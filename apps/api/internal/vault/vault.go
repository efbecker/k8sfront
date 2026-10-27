// Package vault encrypts secrets at rest with AES-256-GCM.
//
// Sealed format (all binary):
//
//	version(1) | keyID(4) | nonce(12) | ciphertext+tag
//
// The keyID is the first 4 bytes of SHA-256(key) and lets us detect a wrong
// master key with a clear error instead of a generic authentication failure.
// Callers must pass associated data (AAD) that binds the ciphertext to the
// record it belongs to (e.g. "secret:<id>:<kind>"), so a ciphertext copied to
// another row fails to decrypt.
package vault

import (
	"bytes"
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

const (
	formatVersion = 0x01
	keySize       = 32
	keyIDSize     = 4
	nonceSize     = 12
	headerSize    = 1 + keyIDSize + nonceSize
)

var (
	ErrInvalidKey   = errors.New("vault: master key must be exactly 32 bytes")
	ErrKeyMismatch  = errors.New("vault: ciphertext was sealed with a different master key")
	ErrMalformed    = errors.New("vault: malformed ciphertext")
	ErrDecryptAuth  = errors.New("vault: authentication failed (tampered data or wrong associated data)")
	ErrEmptyAADUsed = errors.New("vault: associated data is required")
)

// Vault seals and opens secrets with a single AES-256-GCM master key.
type Vault struct {
	aead  cipher.AEAD
	keyID [keyIDSize]byte
}

// New builds a Vault from a raw 32-byte key.
func New(key []byte) (*Vault, error) {
	if len(key) != keySize {
		return nil, ErrInvalidKey
	}
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, fmt.Errorf("vault: %w", err)
	}
	aead, err := cipher.NewGCM(block)
	if err != nil {
		return nil, fmt.Errorf("vault: %w", err)
	}
	v := &Vault{aead: aead}
	sum := sha256.Sum256(key)
	copy(v.keyID[:], sum[:keyIDSize])
	return v, nil
}

// KeyID returns a short, non-secret identifier of the master key.
func (v *Vault) KeyID() string { return hex.EncodeToString(v.keyID[:]) }

// Seal encrypts plaintext bound to aad.
func (v *Vault) Seal(plaintext, aad []byte) ([]byte, error) {
	if len(aad) == 0 {
		return nil, ErrEmptyAADUsed
	}
	out := make([]byte, headerSize, headerSize+len(plaintext)+v.aead.Overhead())
	out[0] = formatVersion
	copy(out[1:1+keyIDSize], v.keyID[:])
	nonce := out[1+keyIDSize : headerSize]
	if _, err := rand.Read(nonce); err != nil {
		return nil, fmt.Errorf("vault: nonce: %w", err)
	}
	return v.aead.Seal(out, nonce, plaintext, aad), nil
}

// Open decrypts a sealed value; aad must match the one used to seal it.
func (v *Vault) Open(sealed, aad []byte) ([]byte, error) {
	if len(aad) == 0 {
		return nil, ErrEmptyAADUsed
	}
	if len(sealed) < headerSize+v.aead.Overhead() || sealed[0] != formatVersion {
		return nil, ErrMalformed
	}
	if !bytes.Equal(sealed[1:1+keyIDSize], v.keyID[:]) {
		return nil, ErrKeyMismatch
	}
	nonce := sealed[1+keyIDSize : headerSize]
	pt, err := v.aead.Open(nil, nonce, sealed[headerSize:], aad)
	if err != nil {
		return nil, ErrDecryptAuth
	}
	return pt, nil
}

// LoadKey resolves the master key. If envValue (base64) is non-empty it wins;
// otherwise the key is read from path, and generated there (0600) when the
// file does not exist yet.
func LoadKey(envValue, path string) ([]byte, error) {
	if s := strings.TrimSpace(envValue); s != "" {
		return decodeKey(s)
	}
	if path == "" {
		return nil, errors.New("vault: no master key configured (set K8SFRONT_MASTER_KEY or a key file path)")
	}
	data, err := os.ReadFile(path)
	if err == nil {
		return decodeKey(strings.TrimSpace(string(data)))
	}
	if !errors.Is(err, os.ErrNotExist) {
		return nil, fmt.Errorf("vault: read key file: %w", err)
	}
	key := make([]byte, keySize)
	if _, err := rand.Read(key); err != nil {
		return nil, fmt.Errorf("vault: generate key: %w", err)
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return nil, fmt.Errorf("vault: create key dir: %w", err)
	}
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return nil, fmt.Errorf("vault: create key file: %w", err)
	}
	defer f.Close()
	if _, err := f.WriteString(base64.StdEncoding.EncodeToString(key) + "\n"); err != nil {
		return nil, fmt.Errorf("vault: write key file: %w", err)
	}
	return key, nil
}

func decodeKey(s string) ([]byte, error) {
	key, err := base64.StdEncoding.DecodeString(s)
	if err != nil {
		return nil, fmt.Errorf("vault: master key is not valid base64: %w", err)
	}
	if len(key) != keySize {
		return nil, ErrInvalidKey
	}
	return key, nil
}

// Wipe zeroes a byte slice holding sensitive material.
func Wipe(b []byte) {
	for i := range b {
		b[i] = 0
	}
}

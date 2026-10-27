package vault

import (
	"bytes"
	"crypto/rand"
	"errors"
	"path/filepath"
	"testing"
)

func newTestVault(t *testing.T) *Vault {
	t.Helper()
	key := make([]byte, 32)
	if _, err := rand.Read(key); err != nil {
		t.Fatal(err)
	}
	v, err := New(key)
	if err != nil {
		t.Fatal(err)
	}
	return v
}

func TestSealOpenRoundTrip(t *testing.T) {
	v := newTestVault(t)
	aad := []byte("secret:abc:ssh_private_key")
	sealed, err := v.Seal([]byte("hunter2"), aad)
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(sealed, []byte("hunter2")) {
		t.Fatal("plaintext visible in ciphertext")
	}
	pt, err := v.Open(sealed, aad)
	if err != nil {
		t.Fatal(err)
	}
	if string(pt) != "hunter2" {
		t.Fatalf("got %q", pt)
	}
}

func TestNonceIsRandom(t *testing.T) {
	v := newTestVault(t)
	a, _ := v.Seal([]byte("x"), []byte("aad"))
	b, _ := v.Seal([]byte("x"), []byte("aad"))
	if bytes.Equal(a, b) {
		t.Fatal("two seals of the same plaintext must differ")
	}
}

func TestOpenRejectsWrongAAD(t *testing.T) {
	v := newTestVault(t)
	sealed, _ := v.Seal([]byte("x"), []byte("secret:1"))
	if _, err := v.Open(sealed, []byte("secret:2")); !errors.Is(err, ErrDecryptAuth) {
		t.Fatalf("want ErrDecryptAuth, got %v", err)
	}
}

func TestOpenRejectsTampering(t *testing.T) {
	v := newTestVault(t)
	sealed, _ := v.Seal([]byte("payload"), []byte("aad"))
	sealed[len(sealed)-1] ^= 0xff
	if _, err := v.Open(sealed, []byte("aad")); !errors.Is(err, ErrDecryptAuth) {
		t.Fatalf("want ErrDecryptAuth, got %v", err)
	}
}

func TestOpenRejectsOtherKey(t *testing.T) {
	a, b := newTestVault(t), newTestVault(t)
	sealed, _ := a.Seal([]byte("x"), []byte("aad"))
	if _, err := b.Open(sealed, []byte("aad")); !errors.Is(err, ErrKeyMismatch) {
		t.Fatalf("want ErrKeyMismatch, got %v", err)
	}
}

func TestRequiresAAD(t *testing.T) {
	v := newTestVault(t)
	if _, err := v.Seal([]byte("x"), nil); !errors.Is(err, ErrEmptyAADUsed) {
		t.Fatal("expected AAD requirement")
	}
}

func TestLoadKeyGeneratesAndReuses(t *testing.T) {
	path := filepath.Join(t.TempDir(), "keys", "master.key")
	k1, err := LoadKey("", path)
	if err != nil {
		t.Fatal(err)
	}
	k2, err := LoadKey("", path)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(k1, k2) || len(k1) != 32 {
		t.Fatal("key file should be generated once and reused")
	}
	if _, err := LoadKey("dG9vc2hvcnQ=", path); !errors.Is(err, ErrInvalidKey) {
		t.Fatalf("short env key must be rejected, got %v", err)
	}
}

// Package passwd hashes local user passwords with argon2id (PHC string format).
package passwd

import (
	"crypto/rand"
	"crypto/subtle"
	"encoding/base64"
	"errors"
	"fmt"
	"strings"
	"unicode/utf8"

	"golang.org/x/crypto/argon2"
)

// Parameters follow the OWASP recommendation for argon2id (19 MiB, t=2, p=1).
const (
	memory  = 19 * 1024
	time    = 2
	threads = 1
	keyLen  = 32
	saltLen = 16
)

// MinLength is the minimum password length for local users.
const MinLength = 10

// Validate checks the password policy.
func Validate(pw string) error {
	if utf8.RuneCountInString(pw) < MinLength {
		return fmt.Errorf("a senha precisa ter ao menos %d caracteres", MinLength)
	}
	if len(pw) > 256 {
		return errors.New("a senha pode ter no máximo 256 bytes")
	}
	return nil
}

// Hash returns "$argon2id$v=19$m=...,t=...,p=...$<salt>$<hash>".
func Hash(pw string) (string, error) {
	salt := make([]byte, saltLen)
	if _, err := rand.Read(salt); err != nil {
		return "", err
	}
	key := argon2.IDKey([]byte(pw), salt, time, memory, threads, keyLen)
	enc := base64.RawStdEncoding
	return fmt.Sprintf("$argon2id$v=%d$m=%d,t=%d,p=%d$%s$%s", argon2.Version, memory, time, threads, enc.EncodeToString(salt), enc.EncodeToString(key)), nil
}

// Verify compares pw against a PHC string in constant time.
func Verify(encoded, pw string) bool {
	parts := strings.Split(encoded, "$")
	if len(parts) != 6 || parts[1] != "argon2id" {
		return false
	}
	var v int
	var m, t uint32
	var p uint8
	if _, err := fmt.Sscanf(parts[2], "v=%d", &v); err != nil || v != argon2.Version {
		return false
	}
	if _, err := fmt.Sscanf(parts[3], "m=%d,t=%d,p=%d", &m, &t, &p); err != nil || m == 0 || m > 1<<20 || t == 0 || t > 16 || p == 0 {
		return false
	}
	enc := base64.RawStdEncoding
	salt, err1 := enc.DecodeString(parts[4])
	want, err2 := enc.DecodeString(parts[5])
	if err1 != nil || err2 != nil || len(want) == 0 || len(want) > 128 {
		return false
	}
	got := argon2.IDKey([]byte(pw), salt, t, m, p, uint32(len(want)))
	return subtle.ConstantTimeCompare(got, want) == 1
}

var dummy, _ = Hash("k8sfront-dummy-password")

// Burn spends the same time as a real verification, so a login for an
// unknown user is not faster than one with a wrong password.
func Burn(pw string) { Verify(dummy, pw) }

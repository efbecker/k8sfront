package sshx

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"net"

	"github.com/efbecker/k8sfront/apps/api/internal/store"
	"golang.org/x/crypto/ssh"
)

// HostKeyStore persists known host keys.
type HostKeyStore interface {
	GetKnownHost(ctx context.Context, hostPort string) (*store.KnownHost, error)
	UpsertKnownHost(ctx context.Context, kh *store.KnownHost) error
}

// HostKeyPolicy decides what happens with an unknown host key.
type HostKeyPolicy int

const (
	// PolicyTOFU registers the key on first contact and enforces it afterwards.
	PolicyTOFU HostKeyPolicy = iota
	// PolicyStrict rejects hosts whose key was not registered beforehand.
	PolicyStrict
)

// HostKeyMismatchError means the server presented a different key than the
// registered one: a possible man-in-the-middle, or a reinstalled host.
type HostKeyMismatchError struct {
	HostPort string
	Expected string
	Got      string
}

func (e *HostKeyMismatchError) Error() string {
	return fmt.Sprintf("host key for %s changed: registered %s, presented %s. Possible man-in-the-middle attack; if the host was reinstalled, reset its trusted key explicitly",
		e.HostPort, e.Expected, e.Got)
}

// ErrUnknownHost is returned under PolicyStrict for unregistered hosts.
var ErrUnknownHost = errors.New("host key not registered and strict host key checking is enabled")

func (d *Dialer) hostKeyCallback(ctx context.Context) ssh.HostKeyCallback {
	return func(hostname string, _ net.Addr, key ssh.PublicKey) error {
		fp := ssh.FingerprintSHA256(key)
		known, err := d.HostKeys.GetKnownHost(ctx, hostname)
		switch {
		case errors.Is(err, store.ErrNotFound):
			if d.Policy == PolicyStrict {
				return fmt.Errorf("%w: %s (%s)", ErrUnknownHost, hostname, fp)
			}
			return d.HostKeys.UpsertKnownHost(ctx, &store.KnownHost{
				HostPort: hostname, KeyType: key.Type(), PublicKey: key.Marshal(), Fingerprint: fp,
			})
		case err != nil:
			return fmt.Errorf("host key lookup: %w", err)
		}
		if !bytes.Equal(known.PublicKey, key.Marshal()) {
			return &HostKeyMismatchError{HostPort: hostname, Expected: known.Fingerprint, Got: fp}
		}
		return d.HostKeys.UpsertKnownHost(ctx, known)
	}
}

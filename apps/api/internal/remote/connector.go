// Package remote opens validated SSH sessions to inventory servers. Every
// connection goes through Open, which enforces the Debian 13+ requirement
// before any other command runs and detects the sudo mode.
package remote

import (
	"context"
	"errors"
	"fmt"

	"github.com/efbecker/k8sfront/apps/api/internal/secrets"
	"github.com/efbecker/k8sfront/apps/api/internal/sshx"
	"github.com/efbecker/k8sfront/apps/api/internal/store"
)

// osReleaseCmd reads os-release without privileges.
const osReleaseCmd = "cat /etc/os-release 2>/dev/null || cat /usr/lib/os-release"

// Connector builds sshx clients from stored servers.
type Connector struct {
	Store   store.Store
	Secrets *secrets.Manager
	Dialer  *sshx.Dialer
}

// Session is a validated connection to a Debian 13+ host.
type Session struct {
	*sshx.Client
	Server *store.Server
	OS     *sshx.DebianInfo
	Sudo   *sshx.SudoReport
}

// Credentials decrypts the credentials of a server. If no dedicated sudo
// password is stored and the server uses password authentication, the SSH
// password doubles as the sudo password (the common Debian setup).
func (c *Connector) Credentials(ctx context.Context, s *store.Server) (sshx.Credentials, error) {
	creds := sshx.Credentials{User: s.SSHUser}
	var err error
	switch s.AuthMethod {
	case store.AuthKey:
		if creds.PrivateKey, err = c.Secrets.GetOptional(ctx, s.CredentialSecretID); err != nil {
			return creds, err
		}
		if creds.Passphrase, err = c.Secrets.GetOptional(ctx, s.PassphraseSecretID); err != nil {
			return creds, err
		}
	case store.AuthPassword:
		if creds.Password, err = c.Secrets.GetOptional(ctx, s.CredentialSecretID); err != nil {
			return creds, err
		}
	default:
		return creds, fmt.Errorf("unknown auth method %q", s.AuthMethod)
	}
	if creds.SudoPassword, err = c.Secrets.GetOptional(ctx, s.SudoSecretID); err != nil {
		return creds, err
	}
	if len(creds.SudoPassword) == 0 && len(creds.Password) > 0 {
		creds.SudoPassword = append([]byte(nil), creds.Password...)
	}
	return creds, nil
}

// Stage identifies where Open failed, so callers can attribute the error.
type Stage string

const (
	StageConnect Stage = "connect"
	StageOS      Stage = "os"
	StageSudo    Stage = "sudo"
)

// OpenError carries the failing stage.
type OpenError struct {
	Stage Stage
	Err   error
}

func (e *OpenError) Error() string { return string(e.Stage) + ": " + e.Err.Error() }
func (e *OpenError) Unwrap() error { return e.Err }

// Hooks lets callers observe each stage (e.g. to drive job steps).
type Hooks struct {
	Before func(stage Stage)
	After  func(stage Stage, detail string, err error)
}

func (h Hooks) before(s Stage) {
	if h.Before != nil {
		h.Before(s)
	}
}

func (h Hooks) after(s Stage, detail string, err error) {
	if h.After != nil {
		h.After(s, detail, err)
	}
}

// Open dials the server, validates Debian 13+ and detects sudo. The returned
// session must be closed by the caller. Server OS and sudo fields are updated
// in the store as a side effect.
func (c *Connector) Open(ctx context.Context, s *store.Server, hooks Hooks) (*Session, error) {
	creds, err := c.Credentials(ctx, s)
	if err != nil {
		return nil, &OpenError{StageConnect, err}
	}
	defer creds.Wipe()

	hooks.before(StageConnect)
	// Dial copies nothing: the client keeps references, so give it its own copy.
	cl, err := c.Dialer.Dial(ctx, sshx.Target{Host: s.Host, Port: s.Port, Creds: cloneCreds(creds), SudoMode: s.SudoMode})
	hooks.after(StageConnect, fmt.Sprintf("SSH %s@%s:%d", s.SSHUser, s.Host, s.Port), err)
	if err != nil {
		return nil, &OpenError{StageConnect, err}
	}
	sess := &Session{Client: cl, Server: s}
	ok := false
	defer func() {
		if !ok {
			cl.Close()
		}
	}()

	hooks.before(StageOS)
	out, _, err := cl.Output(ctx, osReleaseCmd)
	if err == nil {
		sess.OS, err = sshx.ValidateDebian(sshx.ParseOSRelease(out))
		osr := sshx.ParseOSRelease(out)
		s.OSID, s.OSVersionID, s.OSPrettyName = osr["ID"], osr["VERSION_ID"], osr["PRETTY_NAME"]
	}
	detail := s.OSPrettyName
	hooks.after(StageOS, detail, err)
	if err != nil {
		_ = c.Store.UpdateServer(ctx, s)
		return nil, &OpenError{StageOS, err}
	}

	hooks.before(StageSudo)
	sess.Sudo, err = cl.DetectSudo(ctx)
	if err != nil {
		hooks.after(StageSudo, "", err)
		s.SudoMode = store.SudoUnknown
		_ = c.Store.UpdateServer(ctx, s)
		return nil, &OpenError{StageSudo, err}
	}
	hooks.after(StageSudo, sess.Sudo.Message, nil)
	s.SudoMode = sess.Sudo.Mode
	if err := c.Store.UpdateServer(ctx, s); err != nil && !errors.Is(err, store.ErrNotFound) {
		return nil, err
	}
	ok = true
	return sess, nil
}

func cloneCreds(c sshx.Credentials) sshx.Credentials {
	cp := func(b []byte) []byte {
		if b == nil {
			return nil
		}
		return append([]byte(nil), b...)
	}
	return sshx.Credentials{User: c.User, PrivateKey: cp(c.PrivateKey), Passphrase: cp(c.Passphrase), Password: cp(c.Password), SudoPassword: cp(c.SudoPassword)}
}

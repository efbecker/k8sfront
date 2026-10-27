// Package sshx is the SSH execution engine: authenticated connections with
// host key verification, privilege escalation through sudo without exposing
// passwords, and line-streamed, redacted output.
package sshx

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"net"
	"strconv"
	"strings"
	"time"

	"github.com/efbecker/k8sfront/apps/api/internal/redact"
	"github.com/efbecker/k8sfront/apps/api/internal/store"
	"golang.org/x/crypto/ssh"
)

// Credentials are decrypted, in-memory credentials for one connection.
type Credentials struct {
	User         string
	PrivateKey   []byte // PEM / OpenSSH format, Ed25519 or RSA
	Passphrase   []byte // optional, for encrypted private keys
	Password     []byte // SSH password authentication
	SudoPassword []byte // injected via stdin only, never argv
}

// Wipe zeroes all secret material.
func (c *Credentials) Wipe() {
	for _, b := range [][]byte{c.PrivateKey, c.Passphrase, c.Password, c.SudoPassword} {
		for i := range b {
			b[i] = 0
		}
	}
}

// Target identifies a host to connect to.
type Target struct {
	Host     string
	Port     int
	Creds    Credentials
	SudoMode store.SudoMode // known mode; Unknown triggers detection on demand
}

// HostPort returns "host:port" with IPv6 bracketing.
func (t Target) HostPort() string {
	port := t.Port
	if port == 0 {
		port = 22
	}
	return net.JoinHostPort(t.Host, strconv.Itoa(port))
}

// Dialer opens SSH connections.
type Dialer struct {
	HostKeys    HostKeyStore
	Policy      HostKeyPolicy
	Timeout     time.Duration // TCP + handshake timeout
	KeepAlive   time.Duration // interval for keepalive@openssh.com requests
	ClientIdent string
}

// NewDialer returns a Dialer with sane defaults (TOFU, 15s timeout).
func NewDialer(hk HostKeyStore) *Dialer {
	return &Dialer{HostKeys: hk, Policy: PolicyTOFU, Timeout: 15 * time.Second, KeepAlive: 20 * time.Second, ClientIdent: "SSH-2.0-k8sfront"}
}

// ErrAuth wraps SSH authentication failures.
var ErrAuth = errors.New("ssh authentication failed")

func authMethods(c Credentials) ([]ssh.AuthMethod, error) {
	var methods []ssh.AuthMethod
	if len(c.PrivateKey) > 0 {
		var signer ssh.Signer
		var err error
		if len(c.Passphrase) > 0 {
			signer, err = ssh.ParsePrivateKeyWithPassphrase(c.PrivateKey, c.Passphrase)
		} else {
			signer, err = ssh.ParsePrivateKey(c.PrivateKey)
		}
		var missing *ssh.PassphraseMissingError
		if errors.As(err, &missing) {
			return nil, errors.New("the private key is encrypted: provide its passphrase")
		}
		if err != nil {
			return nil, fmt.Errorf("invalid private key: %w", err)
		}
		methods = append(methods, ssh.PublicKeys(signer))
	}
	if len(c.Password) > 0 {
		pw := string(c.Password)
		methods = append(methods,
			ssh.Password(pw),
			ssh.KeyboardInteractive(func(_, _ string, questions []string, _ []bool) ([]string, error) {
				answers := make([]string, len(questions))
				for i := range answers {
					answers[i] = pw
				}
				return answers, nil
			}))
	}
	if len(methods) == 0 {
		return nil, errors.New("no SSH credentials: provide a private key or a password")
	}
	return methods, nil
}

// ValidatePrivateKey checks that a key parses (with its passphrase if given)
// and returns its public key fingerprint.
func ValidatePrivateKey(key, passphrase []byte) (string, error) {
	if _, err := authMethods(Credentials{PrivateKey: key, Passphrase: passphrase}); err != nil {
		return "", err
	}
	var signer ssh.Signer
	var err error
	if len(passphrase) > 0 {
		signer, err = ssh.ParsePrivateKeyWithPassphrase(key, passphrase)
	} else {
		signer, err = ssh.ParsePrivateKey(key)
	}
	if err != nil {
		return "", err
	}
	return ssh.FingerprintSHA256(signer.PublicKey()), nil
}

// Dial connects and authenticates. The context bounds TCP connect and handshake.
func (d *Dialer) Dial(ctx context.Context, t Target) (*Client, error) {
	methods, err := authMethods(t.Creds)
	if err != nil {
		return nil, err
	}
	timeout := d.Timeout
	if timeout == 0 {
		timeout = 15 * time.Second
	}
	cfg := &ssh.ClientConfig{
		User:            t.Creds.User,
		Auth:            methods,
		HostKeyCallback: d.hostKeyCallback(ctx),
		Timeout:         timeout,
		ClientVersion:   d.ClientIdent,
	}
	addr := t.HostPort()
	// Pin negotiation to the registered key type so a server offering several
	// host keys cannot trigger a spurious mismatch.
	if kh, err := d.HostKeys.GetKnownHost(ctx, addr); err == nil {
		cfg.HostKeyAlgorithms = hostKeyAlgorithms(kh.KeyType)
	}
	dctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	var nd net.Dialer
	conn, err := nd.DialContext(dctx, "tcp", addr)
	if err != nil {
		return nil, fmt.Errorf("connect %s: %w", addr, err)
	}
	deadline, _ := dctx.Deadline()
	_ = conn.SetDeadline(deadline)
	sc, chans, reqs, err := ssh.NewClientConn(conn, addr, cfg)
	if err != nil {
		conn.Close()
		var mismatch *HostKeyMismatchError
		if errors.As(err, &mismatch) {
			return nil, mismatch
		}
		if strings.Contains(err.Error(), "unable to authenticate") {
			return nil, fmt.Errorf("%w for %s@%s: check user, key/password", ErrAuth, t.Creds.User, addr)
		}
		return nil, fmt.Errorf("ssh handshake with %s: %w", addr, err)
	}
	_ = conn.SetDeadline(time.Time{})
	c := &Client{
		conn:     ssh.NewClient(sc, chans, reqs),
		target:   t,
		redactor: redact.New(string(t.Creds.Password), string(t.Creds.SudoPassword), string(t.Creds.Passphrase)),
		done:     make(chan struct{}),
	}
	if d.KeepAlive > 0 {
		go c.keepAlive(d.KeepAlive)
	}
	return c, nil
}

// Client is an authenticated SSH connection.
type Client struct {
	conn     *ssh.Client
	target   Target
	redactor *redact.Redactor
	done     chan struct{}
}

// Redactor returns the redactor preloaded with this connection's secrets.
func (c *Client) Redactor() *redact.Redactor { return c.redactor }

// Target returns the connection target (credentials included; do not log).
func (c *Client) Target() Target { return c.target }

// SetSudoMode records the detected escalation mode.
func (c *Client) SetSudoMode(m store.SudoMode) { c.target.SudoMode = m }

// SudoMode returns the escalation mode in use.
func (c *Client) SudoMode() store.SudoMode { return c.target.SudoMode }

// Close closes the connection.
func (c *Client) Close() error {
	select {
	case <-c.done:
	default:
		close(c.done)
		c.target.Creds.Wipe()
	}
	return c.conn.Close()
}

func (c *Client) keepAlive(every time.Duration) {
	t := time.NewTicker(every)
	defer t.Stop()
	for {
		select {
		case <-c.done:
			return
		case <-t.C:
			if _, _, err := c.conn.SendRequest("keepalive@openssh.com", true, nil); err != nil {
				return
			}
		}
	}
}

// runRaw runs an exact command line with optional stdin, without sudo wrapping.
// Only used for fixed, secret-free command lines.
func (c *Client) runRaw(ctx context.Context, cmd string, stdin []byte) (stdout, stderr string, code int, err error) {
	sess, err := c.conn.NewSession()
	if err != nil {
		return "", "", -1, err
	}
	defer sess.Close()
	var out, errb bytes.Buffer
	sess.Stdout, sess.Stderr = &out, &errb
	if stdin != nil {
		sess.Stdin = bytes.NewReader(stdin)
	}
	done := make(chan error, 1)
	go func() { done <- sess.Run(cmd) }()
	select {
	case <-ctx.Done():
		_ = sess.Signal(ssh.SIGKILL)
		sess.Close()
		return "", "", -1, ctx.Err()
	case err = <-done:
	}
	code = exitCode(err)
	if code >= 0 {
		err = nil
	}
	return out.String(), c.redactor.String(errb.String()), code, err
}

func exitCode(err error) int {
	if err == nil {
		return 0
	}
	var ee *ssh.ExitError
	if errors.As(err, &ee) {
		return ee.ExitStatus()
	}
	return -1
}

func hostKeyAlgorithms(keyType string) []string {
	if keyType == ssh.KeyAlgoRSA {
		return []string{ssh.KeyAlgoRSASHA512, ssh.KeyAlgoRSASHA256, ssh.KeyAlgoRSA}
	}
	return []string{keyType}
}

// Output runs a fixed, secret-free command without sudo and returns its raw
// stdout and redacted stderr. Non-zero exit is an error.
func (c *Client) Output(ctx context.Context, cmd string) (string, string, error) {
	out, errOut, code, err := c.runRaw(ctx, cmd, nil)
	if err != nil {
		return out, errOut, err
	}
	if code != 0 {
		return out, errOut, fmt.Errorf("%q exited with status %d: %s", cmd, code, firstLine(strings.TrimSpace(errOut)))
	}
	return out, errOut, nil
}

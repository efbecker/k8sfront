//go:build integration

// Integration tests for the SSH + sudo engine. They need root on a Linux
// machine with sudo and runuser (local users are created on demand), and
// optionally Docker with a debian:trixie image for the Debian 13 end-to-end
// test. Run with:  make test-integration
package integration

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/pem"
	"errors"
	"os"
	"os/exec"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/efbecker/k8sfront/apps/api/internal/redact"
	"github.com/efbecker/k8sfront/apps/api/internal/sshx"
	"github.com/efbecker/k8sfront/apps/api/internal/store"
	"golang.org/x/crypto/ssh"
)

const (
	sudoPW   = "Sudo-P4ss!"
	nosudoPW = "NoSudo-P4ss"
)

type memHostKeys struct {
	mu sync.Mutex
	m  map[string]*store.KnownHost
}

func (m *memHostKeys) GetKnownHost(_ context.Context, hp string) (*store.KnownHost, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if k, ok := m.m[hp]; ok {
		cp := *k
		return &cp, nil
	}
	return nil, store.ErrNotFound
}

func (m *memHostKeys) UpsertKnownHost(_ context.Context, k *store.KnownHost) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.m == nil {
		m.m = map[string]*store.KnownHost{}
	}
	if _, ok := m.m[k.HostPort]; !ok {
		cp := *k
		m.m[k.HostPort] = &cp
	}
	return nil
}

func requireLocalSudo(t *testing.T) {
	t.Helper()
	if os.Geteuid() != 0 {
		t.Skip("needs root to create local test users")
	}
	for _, bin := range []string{"sudo", "runuser", "useradd", "chpasswd"} {
		if _, err := exec.LookPath(bin); err != nil {
			t.Skipf("%s not available", bin)
		}
	}
	setup := `set -e
id nopw >/dev/null 2>&1 || useradd -m -s /bin/bash nopw
echo 'nopw ALL=(ALL) NOPASSWD:ALL' > /etc/sudoers.d/k8sfront-it-nopw
chmod 0440 /etc/sudoers.d/k8sfront-it-nopw
id withpw >/dev/null 2>&1 || useradd -m -s /bin/bash withpw
echo 'withpw:` + sudoPW + `' | chpasswd
echo 'withpw ALL=(ALL) ALL' > /etc/sudoers.d/k8sfront-it-withpw
chmod 0440 /etc/sudoers.d/k8sfront-it-withpw
id nosudo >/dev/null 2>&1 || useradd -m -s /bin/bash nosudo
echo 'nosudo:` + nosudoPW + `' | chpasswd
`
	if out, err := exec.Command("/bin/bash", "-c", setup).CombinedOutput(); err != nil {
		t.Fatalf("user setup failed: %v\n%s", err, out)
	}
}

func newKey(t *testing.T) ([]byte, ssh.PublicKey) {
	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	block, err := ssh.MarshalPrivateKeyWithPassphrase(priv, "", []byte("key-passphrase"))
	if err != nil {
		t.Fatal(err)
	}
	sp, _ := ssh.NewPublicKey(pub)
	return pem.EncodeToMemory(block), sp
}

type lines struct {
	mu sync.Mutex
	l  []string
}

func (l *lines) sink(stream store.LogStream, line string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.l = append(l.l, string(stream)+": "+line)
}

func (l *lines) all() string {
	l.mu.Lock()
	defer l.mu.Unlock()
	return strings.Join(l.l, "\n")
}

func localServer(t *testing.T) (*testSSHServer, []byte) {
	keyPEM, pub := newKey(t)
	srv := &testSSHServer{
		Users:    map[string]string{"root": "root-pw-123", "withpw": sudoPW, "nosudo": nosudoPW},
		AuthKeys: map[string]ssh.PublicKey{"nopw": pub},
		Backend:  runuserBackend,
		HostKey:  newHostKey(t),
	}
	srv.Start(t)
	return srv, keyPEM
}

func dial(t *testing.T, srv *testSSHServer, creds sshx.Credentials) (*sshx.Client, error) {
	host, port := splitAddr(t, srv.Addr)
	d := sshx.NewDialer(&memHostKeys{})
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	return d.Dial(ctx, sshx.Target{Host: host, Port: port, Creds: creds})
}

func TestNoPasswdSudoWithEncryptedEd25519Key(t *testing.T) {
	requireLocalSudo(t)
	srv, key := localServer(t)
	cl, err := dial(t, srv, sshx.Credentials{User: "nopw", PrivateKey: key, Passphrase: []byte("key-passphrase")})
	if err != nil {
		t.Fatal(err)
	}
	defer cl.Close()
	rep, err := cl.DetectSudo(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if rep.Mode != store.SudoNoPasswd {
		t.Fatalf("mode = %s", rep.Mode)
	}
	res, err := cl.Run(context.Background(), "id -u", sshx.RunOptions{Sudo: true}, nil)
	if err != nil || strings.TrimSpace(res.Stdout) != "0" {
		t.Fatalf("sudo run: %v %+v", err, res)
	}
}

func TestSudoPasswordIsInjectedViaStdinOnly(t *testing.T) {
	requireLocalSudo(t)
	srv, _ := localServer(t)
	cl, err := dial(t, srv, sshx.Credentials{User: "withpw", Password: []byte(sudoPW), SudoPassword: []byte(sudoPW)})
	if err != nil {
		t.Fatal(err)
	}
	defer cl.Close()
	rep, err := cl.DetectSudo(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if rep.Mode != store.SudoPassword {
		t.Fatalf("mode = %s", rep.Mode)
	}
	var out lines
	script := `id -u
echo "HISTFILE=$HISTFILE DEBIAN_FRONTEND=$DEBIAN_FRONTEND"
echo "leak attempt: ` + sudoPW + `"
ps -eo args | grep -c -- "` + sudoPW + `" || true
cat   # must read /dev/null, not the rest of the script
echo done`
	res, err := cl.Run(context.Background(), script, sshx.RunOptions{Sudo: true}, out.sink)
	if err != nil {
		t.Fatalf("run: %v\n%s", err, out.all())
	}
	got := out.all()
	if !strings.HasPrefix(strings.TrimSpace(res.Stdout), "0") {
		t.Fatalf("not root: %q", res.Stdout)
	}
	if strings.Contains(got, sudoPW) {
		t.Fatalf("sudo password leaked into streamed output:\n%s", got)
	}
	if !strings.Contains(got, "leak attempt: "+redact.Mask) {
		t.Fatalf("password echo was not redacted:\n%s", got)
	}
	if !strings.Contains(got, "HISTFILE=/dev/null DEBIAN_FRONTEND=noninteractive") {
		t.Fatalf("preamble not applied:\n%s", got)
	}
	if !strings.Contains(got, "done") {
		t.Fatalf("script did not finish:\n%s", got)
	}
	for _, c := range srv.Commands {
		if strings.Contains(c, sudoPW) {
			t.Fatalf("password visible in remote argv: %q", c)
		}
	}
}

func TestWrongSudoPasswordFailsFastWithoutLeaking(t *testing.T) {
	requireLocalSudo(t)
	srv, _ := localServer(t)
	const wrong = "definitely-wrong-pw"
	cl, err := dial(t, srv, sshx.Credentials{User: "withpw", Password: []byte(sudoPW), SudoPassword: []byte(wrong)})
	if err != nil {
		t.Fatal(err)
	}
	defer cl.Close()
	if _, err := cl.DetectSudo(context.Background()); !errors.Is(err, sshx.ErrSudoAuth) {
		t.Fatalf("want ErrSudoAuth, got %v", err)
	}
	// Even when forced into password mode, Run must fail quickly and never
	// hand the script to sudo as further password attempts.
	cl.SetSudoMode(store.SudoPassword)
	var out lines
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	_, err = cl.Run(ctx, "echo should-not-run", sshx.RunOptions{Sudo: true}, out.sink)
	if !errors.Is(err, sshx.ErrSudoAuth) {
		t.Fatalf("want ErrSudoAuth, got %v\n%s", err, out.all())
	}
	if strings.Contains(out.all(), "should-not-run") || strings.Contains(out.all(), wrong) {
		t.Fatalf("unexpected output:\n%s", out.all())
	}
}

func TestUserWithoutSudoIsRejected(t *testing.T) {
	requireLocalSudo(t)
	srv, _ := localServer(t)
	cl, err := dial(t, srv, sshx.Credentials{User: "nosudo", Password: []byte(nosudoPW), SudoPassword: []byte(nosudoPW)})
	if err != nil {
		t.Fatal(err)
	}
	defer cl.Close()
	if _, err := cl.DetectSudo(context.Background()); !errors.Is(err, sshx.ErrSudoAuth) {
		t.Fatalf("want ErrSudoAuth, got %v", err)
	}
}

func TestRootAndCancellation(t *testing.T) {
	requireLocalSudo(t)
	srv, _ := localServer(t)
	cl, err := dial(t, srv, sshx.Credentials{User: "root", Password: []byte("root-pw-123")})
	if err != nil {
		t.Fatal(err)
	}
	defer cl.Close()
	rep, err := cl.DetectSudo(context.Background())
	if err != nil || rep.Mode != store.SudoRoot {
		t.Fatalf("root detection: %v %+v", err, rep)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 500*time.Millisecond)
	defer cancel()
	start := time.Now()
	_, err = cl.Run(ctx, "sleep 30", sshx.RunOptions{Sudo: true}, nil)
	if !errors.Is(err, context.DeadlineExceeded) || time.Since(start) > 5*time.Second {
		t.Fatalf("cancellation: err=%v after %s", err, time.Since(start))
	}
	var ee *sshx.ExitError
	if _, err := cl.Run(context.Background(), "exit 7", sshx.RunOptions{}, nil); !errors.As(err, &ee) || ee.Code != 7 {
		t.Fatalf("exit code: %v", err)
	}
}

func TestBadPasswordAndHostKeyMismatch(t *testing.T) {
	requireLocalSudo(t)
	srv, _ := localServer(t)
	if _, err := dial(t, srv, sshx.Credentials{User: "withpw", Password: []byte("nope")}); !errors.Is(err, sshx.ErrAuth) {
		t.Fatalf("want ErrAuth, got %v", err)
	}
	host, port := splitAddr(t, srv.Addr)
	hk := &memHostKeys{}
	// Pre-register a different key for this address: a MITM or reinstalled host.
	other := newHostKey(t).PublicKey()
	_ = hk.UpsertKnownHost(context.Background(), &store.KnownHost{HostPort: srv.Addr, KeyType: other.Type(),
		PublicKey: other.Marshal(), Fingerprint: ssh.FingerprintSHA256(other)})
	_, err := sshx.NewDialer(hk).Dial(context.Background(), sshx.Target{Host: host, Port: port,
		Creds: sshx.Credentials{User: "withpw", Password: []byte(sudoPW)}})
	var mm *sshx.HostKeyMismatchError
	if !errors.As(err, &mm) {
		t.Fatalf("want HostKeyMismatchError, got %v", err)
	}
	// Strict policy refuses unknown hosts.
	d := sshx.NewDialer(&memHostKeys{})
	d.Policy = sshx.PolicyStrict
	if _, err := d.Dial(context.Background(), sshx.Target{Host: host, Port: port,
		Creds: sshx.Credentials{User: "withpw", Password: []byte(sudoPW)}}); !errors.Is(err, sshx.ErrUnknownHost) {
		t.Fatalf("want ErrUnknownHost, got %v", err)
	}
}

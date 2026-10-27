package sshx

import (
	"bufio"
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/efbecker/k8sfront/apps/api/internal/redact"
	"github.com/efbecker/k8sfront/apps/api/internal/store"
	"golang.org/x/crypto/ssh"
)

// Sink receives redacted output lines.
type Sink func(stream store.LogStream, line string)

// RunOptions controls a remote script execution.
type RunOptions struct {
	Sudo bool              // run as root (directly, NOPASSWD sudo, or sudo -S)
	Env  map[string]string // extra exported variables (values are shell-quoted)
	// CaptureLimit bounds the raw stdout kept in Result.Stdout (default 1 MiB).
	CaptureLimit int
}

// Result of a remote execution.
type Result struct {
	ExitCode int
	// Stdout is the raw (unredacted) captured stdout, for parsing by callers.
	// Never forward it to logs or the UI without redaction.
	Stdout string
}

// ExitError is returned when the script exits non-zero.
type ExitError struct{ Code int }

func (e *ExitError) Error() string {
	return fmt.Sprintf("remote command exited with status %d", e.Code)
}

var (
	ErrSudoNotDetected = errors.New("sudo mode not detected yet: run sudo detection first")
	ErrSudoAuth        = errors.New("privilege escalation failed")
)

// The preamble every script runs with. HISTFILE=/dev/null guarantees nothing
// lands in shell history even if a profile turns history on.
const preamble = `set -o pipefail
export HISTFILE=/dev/null HISTSIZE=0 LC_ALL=C.UTF-8 LANG=C.UTF-8
export DEBIAN_FRONTEND=noninteractive NEEDRESTART_MODE=a APT_LISTCHANGES_FRONTEND=none
export PATH=/usr/local/sbin:/usr/local/bin:/usr/sbin:/usr/bin:/sbin:/bin
`

// ShellQuote single-quotes s for POSIX shells.
func ShellQuote(s string) string {
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}

// BuildScript wraps a script so it is fully parsed before execution and its
// commands cannot consume the remainder of stdin.
func BuildScript(script string, env map[string]string) string {
	var b strings.Builder
	b.WriteString(preamble)
	keys := make([]string, 0, len(env))
	for k := range env {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		fmt.Fprintf(&b, "export %s=%s\n", k, ShellQuote(env[k]))
	}
	b.WriteString("__k8sfront_main() {\n")
	b.WriteString(script)
	if !strings.HasSuffix(script, "\n") {
		b.WriteByte('\n')
	}
	b.WriteString("}\n__k8sfront_main </dev/null\n")
	return b.String()
}

// commandLine returns the remote command line for the given escalation. The
// script itself always travels over stdin, so argv (visible in `ps aux`) only
// ever contains this fixed, secret-free wrapper.
func commandLine(mode store.SudoMode, sudo bool, marker string) (string, bool, error) {
	inner := ShellQuote(fmt.Sprintf("echo %s >&2; exec /bin/bash --noprofile --norc -s", marker))
	shell := "/bin/bash --noprofile --norc -c " + inner
	if !sudo || mode == store.SudoRoot {
		return shell, false, nil
	}
	switch mode {
	case store.SudoNoPasswd:
		return "LC_ALL=C sudo -n -H -- " + shell, false, nil
	case store.SudoPassword:
		// -k ignores cached credentials so the password line written to stdin
		// is always consumed by sudo and never reaches bash. -p '' keeps the
		// prompt out of the output. LC_ALL=C keeps sudo's messages parseable.
		return "LC_ALL=C sudo -k -S -p '' -H -- " + shell, true, nil
	}
	return "", false, ErrSudoNotDetected
}

// Run executes script on the host. Output lines are redacted and sent to sink.
func (c *Client) Run(ctx context.Context, script string, opts RunOptions, sink Sink) (*Result, error) {
	if sink == nil {
		sink = func(store.LogStream, string) {}
	}
	if opts.CaptureLimit <= 0 {
		opts.CaptureLimit = 1 << 20
	}
	marker := newMarker()
	cmd, needsPassword, err := commandLine(c.target.SudoMode, opts.Sudo, marker)
	if err != nil {
		return nil, err
	}
	if needsPassword && len(c.target.Creds.SudoPassword) == 0 {
		return nil, fmt.Errorf("%w: sudo requires a password but none is configured", ErrSudoAuth)
	}

	sess, err := c.conn.NewSession()
	if err != nil {
		return nil, fmt.Errorf("open ssh session: %w", err)
	}
	defer sess.Close()
	stdin, err := sess.StdinPipe()
	if err != nil {
		return nil, err
	}
	stdoutR, err := sess.StdoutPipe()
	if err != nil {
		return nil, err
	}
	stderrR, err := sess.StderrPipe()
	if err != nil {
		return nil, err
	}
	if err := sess.Start(cmd); err != nil {
		return nil, fmt.Errorf("start remote command: %w", err)
	}

	if needsPassword {
		pw := make([]byte, 0, len(c.target.Creds.SudoPassword)+1)
		pw = append(pw, c.target.Creds.SudoPassword...)
		pw = append(pw, '\n')
		_, werr := stdin.Write(pw)
		for i := range pw {
			pw[i] = 0
		}
		if werr != nil {
			return nil, fmt.Errorf("write sudo password: %w", werr)
		}
	}

	ready := make(chan struct{})
	var started atomic.Bool   // the marker was printed: our shell is running
	var preReady bytes.Buffer // stderr seen before the marker (sudo errors)
	var wg sync.WaitGroup
	capture := &limitedBuffer{limit: opts.CaptureLimit}

	wg.Add(2)
	go func() {
		defer wg.Done()
		lw := redact.NewLineWriter(c.redactor, func(l string) { sink(store.StreamStdout, l) })
		_, _ = io.Copy(io.MultiWriter(lw, capture), stdoutR)
		lw.Flush()
	}()
	go func() {
		defer wg.Done()
		lw := redact.NewLineWriter(c.redactor, func(l string) { sink(store.StreamStderr, l) })
		sc := bufio.NewScanner(stderrR)
		sc.Buffer(make([]byte, 64<<10), 1<<20)
		seen := false
		for sc.Scan() {
			line := sc.Text()
			if !seen {
				if strings.TrimSpace(line) == marker {
					seen = true
					started.Store(true)
					close(ready)
					continue
				}
				preReady.WriteString(c.redactor.String(line) + "\n")
				if needsPassword && isSudoRejection(line) {
					// sudo would now read the next stdin line as another
					// password attempt; close stdin so it gives up at once.
					_ = stdin.Close()
				}
				continue
			}
			lw.Write([]byte(line + "\n"))
		}
		lw.Flush()
		if !seen {
			close(ready) // unblock; waitErr decides the outcome
		}
	}()

	waitCh := make(chan error, 1)
	handshake := time.NewTimer(handshakeTimeout)
	defer handshake.Stop()
	select {
	case <-ready:
	case <-handshake.C:
		_ = sess.Signal(ssh.SIGKILL)
		sess.Close()
		wg.Wait()
		return nil, fmt.Errorf("%w: no response from sudo/shell within %s", ErrSudoAuth, handshakeTimeout)
	case <-ctx.Done():
		_ = sess.Signal(ssh.SIGKILL)
		sess.Close()
		wg.Wait()
		return nil, ctx.Err()
	}

	go func() {
		_, werr := io.WriteString(stdin, BuildScript(script, opts.Env))
		_ = stdin.Close()
		if werr != nil && !errors.Is(werr, io.EOF) {
			sink(store.StreamSystem, "failed to send script: "+werr.Error())
		}
	}()
	go func() {
		wg.Wait()
		waitCh <- sess.Wait()
	}()

	var waitErr error
	select {
	case waitErr = <-waitCh:
	case <-ctx.Done():
		_ = sess.Signal(ssh.SIGKILL)
		sess.Close()
		<-waitCh
		return nil, ctx.Err()
	}

	res := &Result{ExitCode: exitCode(waitErr), Stdout: capture.String()}
	msg := strings.TrimSpace(preReady.String())
	if msg != "" {
		for _, l := range strings.Split(msg, "\n") {
			sink(store.StreamStderr, l)
		}
	}
	if !started.Load() {
		// The marker was never printed: sudo (or the shell) failed before our
		// script could start.
		if opts.Sudo && c.target.SudoMode != store.SudoRoot {
			return res, fmt.Errorf("%w: %s", ErrSudoAuth, firstLine(msg))
		}
		return res, fmt.Errorf("remote shell failed to start: %s", firstLine(msg))
	}
	if waitErr != nil && res.ExitCode < 0 {
		return res, fmt.Errorf("ssh session ended unexpectedly: %w", waitErr)
	}
	if res.ExitCode != 0 {
		return res, &ExitError{Code: res.ExitCode}
	}
	return res, nil
}

// handshakeTimeout bounds the wait for the shell to start (sudo auth included).
const handshakeTimeout = 60 * time.Second

func isSudoRejection(line string) bool {
	return strings.Contains(line, "Sorry, try again") || strings.Contains(line, "incorrect password") ||
		strings.Contains(line, "is not in the sudoers file") || strings.Contains(line, "may not run sudo")
}

func firstLine(s string) string {
	if s == "" {
		return "no output (check that /bin/bash and sudo exist on the host)"
	}
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		return s[:i]
	}
	return s
}

func newMarker() string {
	b := make([]byte, 12)
	_, _ = rand.Read(b)
	return "__K8SFRONT_READY_" + hex.EncodeToString(b) + "__"
}

type limitedBuffer struct {
	buf   bytes.Buffer
	limit int
}

func (l *limitedBuffer) Write(p []byte) (int, error) {
	if room := l.limit - l.buf.Len(); room > 0 {
		if len(p) > room {
			l.buf.Write(p[:room])
		} else {
			l.buf.Write(p)
		}
	}
	return len(p), nil
}

func (l *limitedBuffer) String() string { return l.buf.String() }

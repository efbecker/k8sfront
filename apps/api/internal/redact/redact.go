// Package redact removes secrets from text before it is persisted or streamed.
//
// Two layers are applied: exact-match redaction of secrets registered for the
// current operation (sudo/SSH passwords, passphrases, tokens) and pattern
// redaction for well-known secret shapes (private key blocks, kubeadm
// bootstrap tokens, certificate keys, bearer tokens).
package redact

import (
	"bytes"
	"regexp"
	"sort"
	"strings"
	"sync"
)

// Mask replaces every redacted value.
const Mask = "******"

// minSecretLen avoids masking trivially short values that would shred output.
const minSecretLen = 4

var patterns = []struct {
	re   *regexp.Regexp
	repl string
}{
	{regexp.MustCompile(`(?s)-----BEGIN [A-Z0-9 ]*PRIVATE KEY-----.*?-----END [A-Z0-9 ]*PRIVATE KEY-----`), "-----BEGIN PRIVATE KEY-----" + Mask + "-----END PRIVATE KEY-----"},
	// kubeadm bootstrap token: [a-z0-9]{6}.[a-z0-9]{16}
	{regexp.MustCompile(`\b[a-z0-9]{6}\.[a-z0-9]{16}\b`), Mask},
	{regexp.MustCompile(`(--certificate-key[ =])[a-f0-9]{64}`), "${1}" + Mask},
	{regexp.MustCompile(`(?i)(authorization:\s*bearer\s+)\S+`), "${1}" + Mask},
	{regexp.MustCompile(`(?i)((?:client-key-data|client-certificate-data|token|password)\s*[:=]\s*)\S+`), "${1}" + Mask},
}

// Redactor masks registered secrets and known secret patterns. It is safe for
// concurrent use.
type Redactor struct {
	mu      sync.RWMutex
	secrets []string
}

// New returns a Redactor preloaded with secrets.
func New(secrets ...string) *Redactor {
	r := &Redactor{}
	r.Add(secrets...)
	return r
}

// Add registers additional secrets to mask. Short or empty values are ignored.
func (r *Redactor) Add(secrets ...string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	for _, s := range secrets {
		s = strings.TrimRight(s, "\r\n")
		if len(s) < minSecretLen {
			continue
		}
		r.secrets = append(r.secrets, s)
	}
	// Longest first so a secret that contains another is masked whole.
	sort.Slice(r.secrets, func(i, j int) bool { return len(r.secrets[i]) > len(r.secrets[j]) })
}

// String redacts s.
func (r *Redactor) String(s string) string {
	if r != nil {
		r.mu.RLock()
		for _, sec := range r.secrets {
			if strings.Contains(s, sec) {
				s = strings.ReplaceAll(s, sec, Mask)
			}
		}
		r.mu.RUnlock()
	}
	for _, p := range patterns {
		s = p.re.ReplaceAllString(s, p.repl)
	}
	return s
}

// LineWriter is an io.Writer that buffers until newline, redacts each complete
// line and hands it to emit. Buffering per line guarantees a secret split
// across two Write calls is still redacted. Call Flush when the stream ends.
type LineWriter struct {
	mu     sync.Mutex
	r      *Redactor
	emit   func(line string)
	buf    bytes.Buffer
	maxLen int
}

// NewLineWriter creates a LineWriter. Lines longer than 64 KiB are emitted in
// chunks to bound memory.
func NewLineWriter(r *Redactor, emit func(line string)) *LineWriter {
	return &LineWriter{r: r, emit: emit, maxLen: 64 << 10}
}

func (w *LineWriter) Write(p []byte) (int, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.buf.Write(p)
	for {
		data := w.buf.Bytes()
		i := bytes.IndexByte(data, '\n')
		if i < 0 {
			if w.buf.Len() > w.maxLen {
				w.emitLocked(string(data))
				w.buf.Reset()
			}
			return len(p), nil
		}
		line := string(data[:i])
		w.buf.Next(i + 1)
		w.emitLocked(line)
	}
}

// Flush emits any trailing partial line.
func (w *LineWriter) Flush() {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.buf.Len() > 0 {
		w.emitLocked(w.buf.String())
		w.buf.Reset()
	}
}

func (w *LineWriter) emitLocked(line string) {
	w.emit(w.r.String(strings.TrimRight(line, "\r")))
}

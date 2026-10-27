package redact

import (
	"strings"
	"testing"
)

func TestRegisteredSecretsAreMasked(t *testing.T) {
	r := New("S3cr3t-Sudo!", "ab")
	got := r.String("password is S3cr3t-Sudo! and ab stays")
	if strings.Contains(got, "S3cr3t") {
		t.Fatalf("secret leaked: %q", got)
	}
	if !strings.Contains(got, "ab stays") {
		t.Fatalf("short values must not be masked: %q", got)
	}
}

func TestPatterns(t *testing.T) {
	r := New()
	cases := map[string]string{
		"kubeadm join 10.0.0.1:6443 --token abcdef.0123456789abcdef --discovery-token-ca-cert-hash sha256:x": "abcdef.0123456789abcdef",
		"--certificate-key " + strings.Repeat("a1", 32):                                                      strings.Repeat("a1", 32),
		"Authorization: Bearer eyJhbGciOi":                                                                   "eyJhbGciOi",
		"-----BEGIN OPENSSH PRIVATE KEY-----\nAAAA\n-----END OPENSSH PRIVATE KEY-----":                       "AAAA",
	}
	for in, secret := range cases {
		if out := r.String(in); strings.Contains(out, secret) {
			t.Errorf("pattern not redacted:\n in=%q\nout=%q", in, out)
		}
	}
}

func TestLineWriterRedactsAcrossWrites(t *testing.T) {
	var lines []string
	w := NewLineWriter(New("topsecretpw"), func(l string) { lines = append(lines, l) })
	w.Write([]byte("first top"))
	w.Write([]byte("secretpw line\r\nsecond"))
	w.Flush()
	if len(lines) != 2 {
		t.Fatalf("want 2 lines, got %v", lines)
	}
	if lines[0] != "first "+Mask+" line" || lines[1] != "second" {
		t.Fatalf("unexpected lines: %q", lines)
	}
}

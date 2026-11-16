package upgrade

import (
	"regexp"
	"strings"
	"time"
)

// Cert is one line of `kubeadm certs check-expiration`.
type Cert struct {
	Name      string    `json:"name"`
	Expires   time.Time `json:"expires"`
	Residual  string    `json:"residual"`
	Authority bool      `json:"authority"` // a CA (second table)
	External  bool      `json:"external"`
}

var certLine = regexp.MustCompile(`^(\S+)\s+([A-Z][a-z]{2} \d{1,2}, \d{4} \d{2}:\d{2} UTC)\s+(\S+)\s+(.*?)\s*(yes|no)\s*$`)

// ParseCheckExpiration parses the two tables printed by kubeadm (certificates
// and certificate authorities). Unknown lines are ignored.
func ParseCheckExpiration(out string) []Cert {
	var certs []Cert
	authority := false
	for _, line := range strings.Split(out, "\n") {
		line = strings.TrimRight(line, "\r")
		if strings.HasPrefix(line, "CERTIFICATE AUTHORITY") {
			authority = true
			continue
		}
		if strings.HasPrefix(line, "CERTIFICATE") {
			authority = false
			continue
		}
		m := certLine.FindStringSubmatch(line)
		if m == nil {
			continue
		}
		t, err := time.Parse("Jan 2, 2006 15:04 MST", m[2])
		if err != nil {
			continue
		}
		certs = append(certs, Cert{Name: m[1], Expires: t.UTC(), Residual: m[3], Authority: authority, External: m[5] == "yes"})
	}
	return certs
}

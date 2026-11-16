// Package ospatch parses the APT state reported by the os-check script
// (phase 5: OS patching that preserves apt-mark holds).
package ospatch

import (
	"regexp"
	"strings"
)

// Package is an upgradable package.
type Package struct {
	Name     string `json:"name"`
	Suite    string `json:"suite"`
	From     string `json:"from"`
	To       string `json:"to"`
	Security bool   `json:"security"`
	Held     bool   `json:"held"`
}

// NodeReport is the APT state of one node.
type NodeReport struct {
	ServerID       string    `json:"serverId"`
	Name           string    `json:"name"`
	Kernel         string    `json:"kernel"`
	Packages       []Package `json:"packages"`
	Held           []string  `json:"held"`
	Security       int       `json:"security"`
	RebootRequired bool      `json:"rebootRequired"`
	Error          string    `json:"error,omitempty"`
}

// `apt list --upgradable`: name/suite[,suite] new arch [upgradable from: old]
var pkgLine = regexp.MustCompile(`^K8SFRONT_PKG (\S+?)/(\S+) (\S+) \S+ \[upgradable from: ([^\]]+)\]`)

// Parse reads the K8SFRONT_* lines printed by the os-check script.
func Parse(out string) NodeReport {
	r := NodeReport{Packages: []Package{}, Held: []string{}}
	held := map[string]bool{}
	var pkgs []Package
	for _, line := range strings.Split(out, "\n") {
		line = strings.TrimSpace(line)
		switch {
		case strings.HasPrefix(line, "K8SFRONT_HOLD "):
			name := strings.TrimSpace(strings.TrimPrefix(line, "K8SFRONT_HOLD "))
			held[name] = true
			r.Held = append(r.Held, name)
		case strings.HasPrefix(line, "K8SFRONT_REBOOT="):
			r.RebootRequired = strings.TrimPrefix(line, "K8SFRONT_REBOOT=") == "yes"
		case strings.HasPrefix(line, "K8SFRONT_KERNEL="):
			r.Kernel = strings.TrimPrefix(line, "K8SFRONT_KERNEL=")
		default:
			if m := pkgLine.FindStringSubmatch(line); m != nil {
				pkgs = append(pkgs, Package{Name: m[1], Suite: m[2], To: m[3], From: m[4], Security: strings.Contains(m[2], "-security")})
			}
		}
	}
	for _, p := range pkgs {
		p.Held = held[p.Name]
		if p.Security {
			r.Security++
		}
		r.Packages = append(r.Packages, p)
	}
	return r
}

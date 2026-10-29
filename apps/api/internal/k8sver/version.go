// Package k8sver discovers stable Kubernetes releases and implements the
// version skew rules (one minor at a time) used by bootstrap and upgrades.
package k8sver

import (
	"fmt"
	"strconv"
	"strings"
)

// Version is a Kubernetes release such as v1.37.2.
type Version struct {
	Major, Minor, Patch int
}

// Parse accepts "v1.37.2" or "1.37.2".
func Parse(s string) (Version, error) {
	s = strings.TrimPrefix(strings.TrimSpace(s), "v")
	parts := strings.Split(s, ".")
	if len(parts) != 3 {
		return Version{}, fmt.Errorf("invalid Kubernetes version %q (expected vMAJOR.MINOR.PATCH)", s)
	}
	var v Version
	for i, p := range parts {
		n, err := strconv.Atoi(p)
		if err != nil || n < 0 {
			return Version{}, fmt.Errorf("invalid Kubernetes version %q", s)
		}
		switch i {
		case 0:
			v.Major = n
		case 1:
			v.Minor = n
		case 2:
			v.Patch = n
		}
	}
	return v, nil
}

// String returns "v1.37.2".
func (v Version) String() string { return fmt.Sprintf("v%d.%d.%d", v.Major, v.Minor, v.Patch) }

// Plain returns "1.37.2" (the form used by apt package versions).
func (v Version) Plain() string { return strings.TrimPrefix(v.String(), "v") }

// MinorString returns "v1.37" (the pkgs.k8s.io repository channel).
func (v Version) MinorString() string { return fmt.Sprintf("v%d.%d", v.Major, v.Minor) }

// Compare returns -1, 0 or 1.
func (v Version) Compare(o Version) int {
	for _, d := range [][2]int{{v.Major, o.Major}, {v.Minor, o.Minor}, {v.Patch, o.Patch}} {
		if d[0] < d[1] {
			return -1
		}
		if d[0] > d[1] {
			return 1
		}
	}
	return 0
}

package upgrade

import (
	"strings"

	"github.com/efbecker/k8sfront/apps/api/internal/k8sver"
)

// parseLoose accepts versions with build suffixes such as "v1.37.2+k3s1".
func parseLoose(s string) (k8sver.Version, error) {
	if i := strings.IndexAny(s, "+-"); i > 0 {
		s = s[:i]
	}
	return k8sver.Parse(s)
}

// ParseLoose is parseLoose for other packages.
func ParseLoose(s string) (k8sver.Version, error) { return parseLoose(s) }

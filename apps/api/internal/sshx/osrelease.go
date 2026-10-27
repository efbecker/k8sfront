package sshx

import (
	"bufio"
	"errors"
	"fmt"
	"strconv"
	"strings"
)

// MinDebianMajor is the oldest supported Debian release (13 "trixie").
const MinDebianMajor = 13

// debianCodenames maps release codenames to major versions, for testing/sid
// systems whose os-release has no VERSION_ID.
var debianCodenames = map[string]int{
	"trixie": 13,
	"forky":  14,
	"duke":   15,
}

// ErrUnsupportedOS is returned when a host is not Debian 13 or newer.
var ErrUnsupportedOS = errors.New("unsupported operating system")

// OSRelease is the parsed content of /etc/os-release.
type OSRelease map[string]string

// ParseOSRelease parses os-release(5) KEY=VALUE content.
func ParseOSRelease(content string) OSRelease {
	out := OSRelease{}
	sc := bufio.NewScanner(strings.NewReader(content))
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		k, v, ok := strings.Cut(line, "=")
		if !ok {
			continue
		}
		v = strings.TrimSpace(v)
		if len(v) >= 2 && (v[0] == '"' || v[0] == '\'') && v[len(v)-1] == v[0] {
			v = v[1 : len(v)-1]
			if strings.HasPrefix(line[len(k)+1:], `"`) {
				r := strings.NewReplacer(`\"`, `"`, `\\`, `\`, `\$`, `$`, "\\`", "`")
				v = r.Replace(v)
			}
		}
		out[strings.TrimSpace(k)] = v
	}
	return out
}

// DebianInfo is the validated OS identity.
type DebianInfo struct {
	Major      int    `json:"major"`
	VersionID  string `json:"versionId"`
	Codename   string `json:"codename"`
	PrettyName string `json:"prettyName"`
}

// ValidateDebian enforces ID=debian and VERSION_ID >= 13. Systems without
// VERSION_ID (testing/sid) are accepted only when their codename is known to
// be trixie or newer.
func ValidateDebian(osr OSRelease) (*DebianInfo, error) {
	pretty := osr["PRETTY_NAME"]
	if pretty == "" {
		pretty = strings.TrimSpace(osr["NAME"] + " " + osr["VERSION"])
	}
	if osr["ID"] != "debian" {
		id := osr["ID"]
		if id == "" {
			id = "unknown"
		}
		return nil, fmt.Errorf("%w: found %q (ID=%s); only Debian %d (trixie) or newer is supported", ErrUnsupportedOS, pretty, id, MinDebianMajor)
	}
	info := &DebianInfo{VersionID: osr["VERSION_ID"], Codename: osr["VERSION_CODENAME"], PrettyName: pretty}
	if info.VersionID != "" {
		majorStr, _, _ := strings.Cut(info.VersionID, ".")
		major, err := strconv.Atoi(majorStr)
		if err != nil {
			return nil, fmt.Errorf("%w: cannot parse VERSION_ID=%q", ErrUnsupportedOS, info.VersionID)
		}
		info.Major = major
	} else if m, ok := debianCodenames[info.Codename]; ok {
		info.Major = m
	} else {
		return nil, fmt.Errorf("%w: Debian without VERSION_ID and unknown codename %q; only Debian %d (trixie) or newer is supported", ErrUnsupportedOS, info.Codename, MinDebianMajor)
	}
	if info.Major < MinDebianMajor {
		return nil, fmt.Errorf("%w: found Debian %d (%s); Debian %d (trixie) or newer is required", ErrUnsupportedOS, info.Major, pretty, MinDebianMajor)
	}
	return info, nil
}

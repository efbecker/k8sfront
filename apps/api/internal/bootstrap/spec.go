// Package bootstrap renders the idempotent Debian 13+ scripts and kubeadm
// configuration used to provision a Kubernetes cluster. It contains no I/O:
// the ops package executes what this package renders.
package bootstrap

import (
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/netip"
	"regexp"
	"strconv"
	"strings"

	"github.com/efbecker/k8sfront/apps/api/internal/k8sver"
)

// CNI plugins supported by the wizard.
type CNI string

const (
	CNICilium CNI = "cilium"
	CNICalico CNI = "calico"
)

// Pinned add-on versions (overridable per cluster through Options).
const (
	DefaultCiliumVersion    = "1.20.4"
	DefaultCiliumCLIVersion = "v0.20.2"
	DefaultCalicoVersion    = "v3.32.3"
	DefaultMetricsServer    = "v0.8.1"
	DefaultKubeVIPVersion   = "v1.3.1"
	APIServerPort           = 6443
	CRISocket               = "unix:///run/containerd/containerd.sock"
)

// Topologies.
const (
	TopologySingle = "single-node"
	TopologyMulti  = "multi-node"
)

// KubeVIP configures a floating control-plane VIP (ARP mode, static pods).
type KubeVIP struct {
	Address   string `json:"address"`
	Interface string `json:"interface,omitempty"` // empty = interface of the default route
}

// Options are the wizard choices stored as clusters.options.
type Options struct {
	UntaintControlPlane bool     `json:"untaintControlPlane"`
	MetricsServer       bool     `json:"metricsServer"`
	KubeVIP             *KubeVIP `json:"kubeVip,omitempty"`
	CNIVersion          string   `json:"cniVersion,omitempty"`
	ExtraSANs           []string `json:"extraSans,omitempty"`
}

// ParseOptions decodes stored options.
func ParseOptions(raw json.RawMessage) (Options, error) {
	var o Options
	if len(raw) == 0 {
		return o, nil
	}
	err := json.Unmarshal(raw, &o)
	return o, err
}

// Node is a server taking part in the cluster.
type Node struct {
	ID   string
	Name string
	Host string
}

// Spec is everything needed to render the bootstrap.
type Spec struct {
	ClusterName   string
	Version       k8sver.Version
	PodCIDR       string
	ServiceCIDR   string
	CNI           CNI
	Topology      string
	Options       Options
	ControlPlanes []Node // ControlPlanes[0] runs kubeadm init
	Workers       []Node
}

// Endpoint returns the controlPlaneEndpoint host:port (VIP when kube-vip is
// enabled, otherwise the first control plane).
func (s *Spec) Endpoint() string {
	host := ""
	if s.Options.KubeVIP != nil && s.Options.KubeVIP.Address != "" {
		host = s.Options.KubeVIP.Address
	} else if len(s.ControlPlanes) > 0 {
		host = s.ControlPlanes[0].Host
	}
	return net.JoinHostPort(host, strconv.Itoa(APIServerPort))
}

// CNIVersion returns the effective CNI version.
func (s *Spec) CNIVersion() string {
	if s.Options.CNIVersion != "" {
		return s.Options.CNIVersion
	}
	if s.CNI == CNICalico {
		return DefaultCalicoVersion
	}
	return DefaultCiliumVersion
}

var (
	nameRe         = regexp.MustCompile(`^[a-z0-9]([a-z0-9-]{0,61}[a-z0-9])?$`)
	ifaceRe        = regexp.MustCompile(`^[a-zA-Z0-9._-]{1,15}$`)
	addonVerRe     = regexp.MustCompile(`^v?[0-9]+\.[0-9]+\.[0-9]+$`)
	ErrInvalidSpec = errors.New("invalid cluster specification")
)

func invalid(format string, a ...any) error {
	return fmt.Errorf("%w: %s", ErrInvalidSpec, fmt.Sprintf(format, a...))
}

// MinSupportedMinor is the oldest Kubernetes minor the bootstrap targets
// (kubeadm v1beta4 configuration API).
const MinSupportedMinor = 31

// Validate checks the spec for consistency before anything touches a host.
func (s *Spec) Validate() error {
	if !nameRe.MatchString(s.ClusterName) {
		return invalid("cluster name %q must be a DNS label (lowercase letters, digits and '-')", s.ClusterName)
	}
	if s.Version.Major != 1 || s.Version.Minor < MinSupportedMinor {
		return invalid("Kubernetes %s is not supported (minimum v1.%d)", s.Version, MinSupportedMinor)
	}
	if s.CNI != CNICilium && s.CNI != CNICalico {
		return invalid("CNI must be cilium or calico")
	}
	if v := s.Options.CNIVersion; v != "" && !addonVerRe.MatchString(v) {
		return invalid("CNI version %q is not a semantic version", v)
	}
	pod, err := netip.ParsePrefix(s.PodCIDR)
	if err != nil || !pod.Addr().Is4() {
		return invalid("pod CIDR %q must be an IPv4 CIDR", s.PodCIDR)
	}
	svc, err := netip.ParsePrefix(s.ServiceCIDR)
	if err != nil || !svc.Addr().Is4() {
		return invalid("service CIDR %q must be an IPv4 CIDR", s.ServiceCIDR)
	}
	if pod.Overlaps(svc) {
		return invalid("pod CIDR %s overlaps service CIDR %s", pod, svc)
	}
	if pod.Bits() > 24 {
		return invalid("pod CIDR %s is too small (use /24 or larger, e.g. 10.244.0.0/16)", pod)
	}
	if svc.Bits() < 12 || svc.Bits() > 24 {
		return invalid("service CIDR %s must be between /12 and /24", svc)
	}
	if len(s.ControlPlanes) == 0 {
		return invalid("at least one control plane is required")
	}
	switch s.Topology {
	case TopologySingle:
		if len(s.ControlPlanes) != 1 || len(s.Workers) != 0 {
			return invalid("single-node topology takes exactly one control plane and no workers")
		}
	case TopologyMulti:
	default:
		return invalid("topology must be %s or %s", TopologySingle, TopologyMulti)
	}
	if len(s.ControlPlanes) > 1 && (s.Options.KubeVIP == nil || s.Options.KubeVIP.Address == "") {
		return invalid("more than one control plane requires a VIP (kube-vip) as the control plane endpoint")
	}
	if len(s.ControlPlanes) > 1 && len(s.ControlPlanes)%2 == 0 {
		return invalid("use an odd number of control planes (etcd quorum): %d given", len(s.ControlPlanes))
	}
	seen := map[string]bool{}
	for _, n := range append(append([]Node{}, s.ControlPlanes...), s.Workers...) {
		if seen[n.ID] {
			return invalid("server %s appears more than once", n.Name)
		}
		seen[n.ID] = true
		if ip, err := netip.ParseAddr(n.Host); err == nil && (pod.Contains(ip) || svc.Contains(ip)) {
			return invalid("node address %s is inside the pod or service CIDR", n.Host)
		}
	}
	if kv := s.Options.KubeVIP; kv != nil && kv.Address != "" {
		ip, err := netip.ParseAddr(kv.Address)
		if err != nil || !ip.Is4() {
			return invalid("kube-vip address %q must be an IPv4 address", kv.Address)
		}
		if pod.Contains(ip) || svc.Contains(ip) {
			return invalid("kube-vip address %s is inside the pod or service CIDR", ip)
		}
		for _, n := range s.ControlPlanes {
			if n.Host == kv.Address {
				return invalid("kube-vip address must not be a node address")
			}
		}
		if kv.Interface != "" && !ifaceRe.MatchString(kv.Interface) {
			return invalid("invalid network interface name %q", kv.Interface)
		}
	}
	for _, san := range s.Options.ExtraSANs {
		if _, err := netip.ParseAddr(san); err != nil && !nameRe.MatchString(strings.ReplaceAll(san, ".", "")) {
			return invalid("invalid certificate SAN %q", san)
		}
	}
	return nil
}

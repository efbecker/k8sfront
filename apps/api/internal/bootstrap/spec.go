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
	DefaultMetalLBVersion   = "v0.15.2"
	APIServerPort           = 6443
	// LocalProxyPort is where the per-node API proxy listens (127.0.0.1 only).
	// 6443 is taken by kube-apiserver on control planes.
	LocalProxyPort = 7445
	// LocalProxyHost is the controlPlaneEndpoint name in local-proxy mode. Every
	// node maps it to 127.0.0.1 in /etc/hosts; it is a SAN of the API cert.
	LocalProxyHost = "k8s-api.k8sfront.internal"
	CRISocket      = "unix:///run/containerd/containerd.sock"
)

// Topologies.
const (
	TopologySingle = "single-node"
	TopologyMulti  = "multi-node"
)

// EndpointMode is how nodes reach a highly available kube-apiserver.
type EndpointMode string

const (
	// EndpointLocalProxy (default): every node runs HAProxy on
	// 127.0.0.1:LocalProxyPort with health checks to all control planes
	// (like Talos KubePrism / RKE2). No VIP, no external load balancer.
	EndpointLocalProxy EndpointMode = "local-proxy"
	// EndpointKubeVIP: floating VIP announced by kube-vip (ARP).
	EndpointKubeVIP EndpointMode = "kube-vip"
	// EndpointDirect: the first control plane's address (single control
	// plane only). Clusters created before endpoint modes existed use it.
	EndpointDirect EndpointMode = "direct"
)

// KubeVIP configures a floating control-plane VIP (ARP mode, static pods).
type KubeVIP struct {
	Address   string `json:"address"`
	Interface string `json:"interface,omitempty"` // empty = interface of the default route
}

// Options are the wizard choices stored as clusters.options.
type Options struct {
	UntaintControlPlane bool `json:"untaintControlPlane"`
	MetricsServer       bool `json:"metricsServer"`
	// EndpointMode is empty on clusters created before it existed: kube-vip
	// when KubeVIP is set, direct otherwise. New clusters default to local-proxy.
	EndpointMode EndpointMode `json:"endpointMode,omitempty"`
	KubeVIP      *KubeVIP     `json:"kubeVip,omitempty"`
	CNIVersion   string       `json:"cniVersion,omitempty"`
	ExtraSANs    []string     `json:"extraSans,omitempty"`
	// LoadBalancer gives Services of type LoadBalancer a real IP from a
	// range of the node network (nil = none: they stay Pending).
	LoadBalancer *LoadBalancer `json:"loadBalancer,omitempty"`
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

// Mode returns the effective endpoint mode.
func (o Options) Mode() EndpointMode {
	if o.EndpointMode != "" {
		return o.EndpointMode
	}
	if o.KubeVIP != nil && o.KubeVIP.Address != "" {
		return EndpointKubeVIP
	}
	return EndpointDirect
}

// Mode returns the effective endpoint mode of the cluster.
func (s *Spec) Mode() EndpointMode { return s.Options.Mode() }

// LocalProxy reports whether nodes reach the API through the local proxy.
func (s *Spec) LocalProxy() bool { return s.Mode() == EndpointLocalProxy }

// Endpoint returns the controlPlaneEndpoint host:port: the local proxy name,
// the kube-vip VIP or the first control plane.
func (s *Spec) Endpoint() string {
	switch s.Mode() {
	case EndpointLocalProxy:
		return net.JoinHostPort(LocalProxyHost, strconv.Itoa(LocalProxyPort))
	case EndpointKubeVIP:
		if s.Options.KubeVIP != nil {
			return net.JoinHostPort(s.Options.KubeVIP.Address, strconv.Itoa(APIServerPort))
		}
	}
	host := ""
	if len(s.ControlPlanes) > 0 {
		host = s.ControlPlanes[0].Host
	}
	return net.JoinHostPort(host, strconv.Itoa(APIServerPort))
}

// ExternalEndpoints are the addresses k8sfront and an external kubectl use to
// reach the API (they have no local proxy): the VIP, or every control plane.
func (s *Spec) ExternalEndpoints() []string {
	if s.Mode() == EndpointKubeVIP {
		return []string{s.Endpoint()}
	}
	out := make([]string, 0, len(s.ControlPlanes))
	for _, n := range s.ControlPlanes {
		out = append(out, net.JoinHostPort(n.Host, strconv.Itoa(APIServerPort)))
	}
	return out
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
	switch s.Mode() {
	case EndpointLocalProxy:
		if s.Options.KubeVIP != nil && s.Options.KubeVIP.Address != "" {
			return invalid("the local API proxy and kube-vip are mutually exclusive")
		}
	case EndpointKubeVIP:
		if s.Options.KubeVIP == nil || s.Options.KubeVIP.Address == "" {
			return invalid("kube-vip mode requires the VIP address")
		}
	case EndpointDirect:
		if len(s.ControlPlanes) > 1 {
			return invalid("more than one control plane requires the local API proxy or a VIP (kube-vip) as the control plane endpoint")
		}
	default:
		return invalid("endpoint mode must be %s, %s or %s", EndpointLocalProxy, EndpointKubeVIP, EndpointDirect)
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
	if lb := s.Options.LoadBalancer; lb != nil {
		if err := s.validateLoadBalancer(lb); err != nil {
			return err
		}
	}
	for _, san := range s.Options.ExtraSANs {
		if _, err := netip.ParseAddr(san); err != nil && !nameRe.MatchString(strings.ReplaceAll(san, ".", "")) {
			return invalid("invalid certificate SAN %q", san)
		}
	}
	return nil
}

// ValidateNew adds the checks that only apply when a cluster is created:
// an odd number of control planes (etcd quorum). Lifecycle jobs accept even
// counts, which happen while control planes are added or removed one by one.
func (s *Spec) ValidateNew() error {
	if err := s.Validate(); err != nil {
		return err
	}
	if len(s.ControlPlanes) > 1 && len(s.ControlPlanes)%2 == 0 {
		return invalid("use an odd number of control planes (etcd quorum): %d given", len(s.ControlPlanes))
	}
	return nil
}

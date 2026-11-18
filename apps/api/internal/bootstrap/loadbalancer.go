package bootstrap

import (
	"fmt"
	"net/netip"
	"strings"
)

// LBProvider is the bare-metal implementation of Services type=LoadBalancer.
type LBProvider string

// LBMetalLB announces the addresses with MetalLB in L2 mode (ARP), which
// works with both CNIs and with kube-proxy. Cilium's own L2 announcements
// require replacing kube-proxy, which the bootstrap keeps.
const LBMetalLB LBProvider = "metallb"

// MaxLBAddresses bounds the size of the address pool.
const MaxLBAddresses = 4096

// LoadBalancer is the address pool handed to LoadBalancer Services.
type LoadBalancer struct {
	Provider LBProvider `json:"provider"`
	// Addresses are IPv4 ranges ("192.168.1.200-192.168.1.220") or CIDRs.
	Addresses []string `json:"addresses"`
}

// AddrRange is an inclusive IPv4 range.
type AddrRange struct{ From, To netip.Addr }

func (r AddrRange) contains(a netip.Addr) bool {
	return a.Is4() && r.From.Compare(a) <= 0 && a.Compare(r.To) <= 0
}

func (r AddrRange) size() uint64 { return uint64(u32(r.To)-u32(r.From)) + 1 }

func (r AddrRange) String() string {
	if r.From == r.To {
		return r.From.String() + "/32"
	}
	return r.From.String() + "-" + r.To.String()
}

func u32(a netip.Addr) uint32 {
	b := a.As4()
	return uint32(b[0])<<24 | uint32(b[1])<<16 | uint32(b[2])<<8 | uint32(b[3])
}

func prefixRange(p netip.Prefix) AddrRange {
	p = p.Masked()
	last := u32(p.Addr()) | (uint32(1)<<(32-p.Bits()) - 1)
	if p.Bits() == 0 {
		last = ^uint32(0)
	}
	return AddrRange{From: p.Addr(), To: netip.AddrFrom4([4]byte{byte(last >> 24), byte(last >> 16), byte(last >> 8), byte(last)})}
}

// ParseAddrRange accepts "a.b.c.d-e.f.g.h", "a.b.c.d-h" (same /24), a CIDR
// or a single address.
func ParseAddrRange(s string) (AddrRange, error) {
	s = strings.TrimSpace(s)
	if from, to, ok := strings.Cut(s, "-"); ok {
		a, err := netip.ParseAddr(strings.TrimSpace(from))
		if err != nil || !a.Is4() {
			return AddrRange{}, fmt.Errorf("%q: início da faixa não é um IPv4", s)
		}
		to = strings.TrimSpace(to)
		if !strings.Contains(to, ".") {
			oct := a.As4()
			to = fmt.Sprintf("%d.%d.%d.%s", oct[0], oct[1], oct[2], to)
		}
		b, err := netip.ParseAddr(to)
		if err != nil || !b.Is4() {
			return AddrRange{}, fmt.Errorf("%q: fim da faixa não é um IPv4", s)
		}
		if b.Less(a) {
			return AddrRange{}, fmt.Errorf("%q: o fim da faixa vem antes do início", s)
		}
		return AddrRange{From: a, To: b}, nil
	}
	if p, err := netip.ParsePrefix(s); err == nil {
		if !p.Addr().Is4() {
			return AddrRange{}, fmt.Errorf("%q: use IPv4", s)
		}
		return prefixRange(p), nil
	}
	if a, err := netip.ParseAddr(s); err == nil && a.Is4() {
		return AddrRange{From: a, To: a}, nil
	}
	return AddrRange{}, fmt.Errorf("%q: use uma faixa (192.168.1.200-192.168.1.220), um CIDR ou um IP", s)
}

// Ranges parses the pool (already validated by Spec.Validate).
func (lb *LoadBalancer) Ranges() ([]AddrRange, error) {
	out := make([]AddrRange, 0, len(lb.Addresses))
	for _, a := range lb.Addresses {
		r, err := ParseAddrRange(a)
		if err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, nil
}

func (s *Spec) validateLoadBalancer(lb *LoadBalancer) error {
	if lb.Provider != LBMetalLB {
		return invalid("load balancer provider must be %s", LBMetalLB)
	}
	if len(lb.Addresses) == 0 {
		return invalid("informe ao menos uma faixa de IPs para o LoadBalancer")
	}
	ranges, err := lb.Ranges()
	if err != nil {
		return invalid("faixa do LoadBalancer %s", err)
	}
	pod, _ := netip.ParsePrefix(s.PodCIDR)
	svc, _ := netip.ParsePrefix(s.ServiceCIDR)
	var total uint64
	for i, r := range ranges {
		total += r.size()
		for _, c := range []netip.Prefix{pod, svc} {
			cr := prefixRange(c)
			if c.IsValid() && r.From.Compare(cr.To) <= 0 && cr.From.Compare(r.To) <= 0 {
				return invalid("a faixa do LoadBalancer %s cruza o CIDR %s do cluster", r, c)
			}
		}
		for _, o := range ranges[:i] {
			if r.From.Compare(o.To) <= 0 && o.From.Compare(r.To) <= 0 {
				return invalid("as faixas do LoadBalancer %s e %s se sobrepõem", o, r)
			}
		}
		for _, n := range allNodesOf(s) {
			if a, err := netip.ParseAddr(n.Host); err == nil && r.contains(a) {
				return invalid("a faixa do LoadBalancer %s inclui o IP do nó %s (%s)", r, n.Name, n.Host)
			}
		}
		if kv := s.Options.KubeVIP; kv != nil {
			if a, err := netip.ParseAddr(kv.Address); err == nil && r.contains(a) {
				return invalid("a faixa do LoadBalancer %s inclui o VIP do kube-vip %s", r, a)
			}
		}
	}
	if total > MaxLBAddresses {
		return invalid("a faixa do LoadBalancer tem %d endereços (máximo %d)", total, MaxLBAddresses)
	}
	return nil
}

func allNodesOf(s *Spec) []Node { return append(append([]Node{}, s.ControlPlanes...), s.Workers...) }

// LoadBalancerScript installs MetalLB and (re)applies the address pool.
func (s *Spec) LoadBalancerScript() string {
	lb := s.Options.LoadBalancer
	var addrs []string
	if lb != nil {
		ranges, _ := lb.Ranges()
		for _, r := range ranges {
			addrs = append(addrs, r.String()) // canonical form: safe inside the YAML heredoc
		}
	}
	return render("metallb", map[string]any{"Version": DefaultMetalLBVersion, "Addresses": addrs})
}

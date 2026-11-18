package kube

import (
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"time"

	corev1 "k8s.io/api/core/v1"
	networkingv1 "k8s.io/api/networking/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"
)

// Access link kinds.
const (
	AccessLoadBalancer = "loadbalancer"
	AccessExternalIP   = "external-ip"
	AccessNodePort     = "nodeport"
	AccessIngress      = "ingress"
)

// AccessLink is one URL a Service answers on from outside the cluster.
type AccessLink struct {
	URL   string `json:"url"`
	Kind  string `json:"kind"`
	Label string `json:"label"`
	// Port is the Service port the link reaches.
	Port int32 `json:"port"`
	// Node is the node whose address a NodePort link uses.
	Node string `json:"node,omitempty"`
}

// ServiceAccess is how one Service can be reached.
type ServiceAccess struct {
	Namespace string       `json:"namespace"`
	Name      string       `json:"name"`
	Type      string       `json:"type"`
	Links     []AccessLink `json:"links"`
	// Pending: a LoadBalancer Service without an external address yet.
	Pending bool `json:"pending,omitempty"`
	// LocalTraffic: externalTrafficPolicy=Local, so NodePort/LB only answer
	// on nodes that run a ready pod of the Service.
	LocalTraffic bool `json:"localTraffic,omitempty"`
	// Endpoints is the number of ready backend addresses (-1 = unknown).
	Endpoints int      `json:"endpoints"`
	Ingresses []string `json:"ingresses,omitempty"`
}

// LoadBalancerInfo says whether anything hands out LoadBalancer IPs.
type LoadBalancerInfo struct {
	// Provider configured through k8sfront ("metallb") or detected.
	Provider  string   `json:"provider,omitempty"`
	Addresses []string `json:"addresses,omitempty"`
	// Installed: the provider's controller exists in the cluster.
	Installed bool `json:"installed"`
}

// Access is the payload of the "Acesso" panels.
type Access struct {
	Services     []ServiceAccess  `json:"services"`
	LoadBalancer LoadBalancerInfo `json:"loadBalancer"`
}

// ListAccess builds the external links of the Services of ns ("" = all).
// ClusterIP Services only appear when an Ingress routes to them. lb carries
// what k8sfront configured; the MetalLB controller is detected either way.
func ListAccess(ctx context.Context, cs kubernetes.Interface, ns string, lb LoadBalancerInfo) (*Access, error) {
	svcs, err := cs.CoreV1().Services(ns).List(ctx, metav1.ListOptions{})
	if err != nil {
		return nil, err
	}
	nodes, err := cs.CoreV1().Nodes().List(ctx, metav1.ListOptions{})
	if err != nil {
		return nil, err
	}
	var ingresses []networkingv1.Ingress
	if l, err := cs.NetworkingV1().Ingresses(ns).List(ctx, metav1.ListOptions{}); err == nil {
		ingresses = l.Items
	}
	if _, err := cs.AppsV1().Deployments("metallb-system").Get(ctx, "controller", metav1.GetOptions{}); err == nil {
		lb.Installed = true
		if lb.Provider == "" {
			lb.Provider = "metallb"
		}
	}
	ready := readyEndpoints(ctx, cs, ns)
	out := &Access{Services: []ServiceAccess{}, LoadBalancer: lb}
	byName := map[string]int{}
	for i := range svcs.Items {
		s := &svcs.Items[i]
		a := serviceAccess(s, nodes.Items)
		a.Endpoints = -1
		if n, ok := ready[s.Namespace+"/"+s.Name]; ok {
			a.Endpoints = n
		} else if ready != nil {
			a.Endpoints = 0
		}
		byName[s.Namespace+"/"+s.Name] = len(out.Services)
		out.Services = append(out.Services, a)
	}
	for _, in := range ingresses {
		for _, l := range ingressLinks(&in) {
			idx, ok := byName[in.Namespace+"/"+l.service]
			if !ok {
				continue
			}
			sa := &out.Services[idx]
			sa.Links = append(sa.Links, l.link)
			if !contains(sa.Ingresses, in.Name) {
				sa.Ingresses = append(sa.Ingresses, in.Name)
			}
		}
	}
	kept := out.Services[:0]
	for _, sa := range out.Services {
		if len(sa.Links) > 0 || sa.Pending {
			kept = append(kept, sa)
		}
	}
	out.Services = kept
	sort.SliceStable(out.Services, func(i, j int) bool {
		a, b := out.Services[i], out.Services[j]
		if a.Namespace != b.Namespace {
			return a.Namespace < b.Namespace
		}
		return a.Name < b.Name
	})
	return out, nil
}

func contains(list []string, s string) bool {
	for _, v := range list {
		if v == s {
			return true
		}
	}
	return false
}

// readyEndpoints counts ready addresses per Service (nil if unreadable).
func readyEndpoints(ctx context.Context, cs kubernetes.Interface, ns string) map[string]int {
	list, err := cs.DiscoveryV1().EndpointSlices(ns).List(ctx, metav1.ListOptions{})
	if err != nil {
		return nil
	}
	out := map[string]int{}
	for _, sl := range list.Items {
		svc := sl.Labels["kubernetes.io/service-name"]
		if svc == "" {
			continue
		}
		key := sl.Namespace + "/" + svc
		n := out[key]
		for _, e := range sl.Endpoints {
			if e.Conditions.Ready == nil || *e.Conditions.Ready {
				n += len(e.Addresses)
			}
		}
		out[key] = n
	}
	return out
}

// scheme guesses http/https from the port number, name and appProtocol.
func scheme(p corev1.ServicePort) string {
	name := strings.ToLower(p.Name)
	if p.AppProtocol != nil {
		name += " " + strings.ToLower(*p.AppProtocol)
	}
	if p.Port == 443 || p.Port == 8443 || p.Port == 6443 || strings.Contains(name, "https") || strings.Contains(name, "tls") {
		return "https"
	}
	return "http"
}

func linkURL(sch, host string, port int32) string {
	if (sch == "http" && port == 80) || (sch == "https" && port == 443) {
		if strings.Contains(host, ":") {
			host = "[" + host + "]"
		}
		return sch + "://" + host + "/"
	}
	return sch + "://" + net.JoinHostPort(host, strconv.Itoa(int(port))) + "/"
}

// accessPort reports whether a port is worth a link (TCP; UDP/SCTP are not URLs).
func accessPort(p corev1.ServicePort) bool {
	return p.Protocol == "" || p.Protocol == corev1.ProtocolTCP
}

func serviceAccess(s *corev1.Service, nodes []corev1.Node) ServiceAccess {
	a := ServiceAccess{Namespace: s.Namespace, Name: s.Name, Type: string(s.Spec.Type), Links: []AccessLink{},
		LocalTraffic: s.Spec.ExternalTrafficPolicy == corev1.ServiceExternalTrafficPolicyLocal}
	var lbAddrs []string
	for _, ing := range s.Status.LoadBalancer.Ingress {
		if ing.IP != "" {
			lbAddrs = append(lbAddrs, ing.IP)
		} else if ing.Hostname != "" {
			lbAddrs = append(lbAddrs, ing.Hostname)
		}
	}
	if s.Spec.Type == corev1.ServiceTypeLoadBalancer && len(lbAddrs) == 0 {
		a.Pending = true
	}
	for _, p := range s.Spec.Ports {
		if !accessPort(p) {
			continue
		}
		sch := scheme(p)
		for _, ip := range lbAddrs {
			a.Links = append(a.Links, AccessLink{URL: linkURL(sch, ip, p.Port), Kind: AccessLoadBalancer, Label: "IP do LoadBalancer", Port: p.Port})
		}
		for _, ip := range s.Spec.ExternalIPs {
			a.Links = append(a.Links, AccessLink{URL: linkURL(sch, ip, p.Port), Kind: AccessExternalIP, Label: "externalIP", Port: p.Port})
		}
		if p.NodePort == 0 || (s.Spec.Type != corev1.ServiceTypeNodePort && s.Spec.Type != corev1.ServiceTypeLoadBalancer) {
			continue
		}
		for _, n := range nodeAddrs(nodes) {
			a.Links = append(a.Links, AccessLink{URL: linkURL(sch, n.ip, p.NodePort), Kind: AccessNodePort,
				Label: "NodePort no nó " + n.name, Port: p.Port, Node: n.name})
		}
	}
	return a
}

type nodeAddr struct{ name, ip string }

// nodeAddrs lists Ready nodes (workers first: control planes are often
// excluded from external traffic) with their InternalIP.
func nodeAddrs(nodes []corev1.Node) []nodeAddr {
	var workers, cps []nodeAddr
	for i := range nodes {
		v := nodeView(&nodes[i])
		if !v.Ready || v.InternalIP == "" {
			continue
		}
		n := nodeAddr{v.Name, v.InternalIP}
		if contains(v.Roles, "control-plane") {
			cps = append(cps, n)
		} else {
			workers = append(workers, n)
		}
	}
	sort.Slice(workers, func(i, j int) bool { return workers[i].name < workers[j].name })
	sort.Slice(cps, func(i, j int) bool { return cps[i].name < cps[j].name })
	return append(workers, cps...)
}

type ingressLink struct {
	service string
	link    AccessLink
}

// ingressLinks returns one link per rule path that targets a Service. Rules
// without host use the Ingress address (or are skipped without one).
func ingressLinks(in *networkingv1.Ingress) []ingressLink {
	tls := map[string]bool{}
	for _, t := range in.Spec.TLS {
		for _, h := range t.Hosts {
			tls[h] = true
		}
	}
	var addr string
	for _, a := range in.Status.LoadBalancer.Ingress {
		if a.IP != "" {
			addr = a.IP
		} else if a.Hostname != "" {
			addr = a.Hostname
		}
		if addr != "" {
			break
		}
	}
	var out []ingressLink
	seen := map[string]bool{}
	add := func(host, path string, b *networkingv1.IngressServiceBackend) {
		if b == nil {
			return
		}
		h := host
		if h == "" {
			h = addr
		}
		if h == "" || strings.HasPrefix(h, "*.") {
			return
		}
		sch := "http"
		if tls[host] || (host == "" && len(in.Spec.TLS) > 0) {
			sch = "https"
		}
		if path == "" || strings.ContainsAny(path, "()[]*?$^\\{}|+") {
			path = "/"
		}
		if strings.Contains(h, ":") {
			h = "[" + h + "]"
		}
		u := sch + "://" + h + path
		if seen[u+b.Name] {
			return
		}
		seen[u+b.Name] = true
		out = append(out, ingressLink{service: b.Name, link: AccessLink{URL: u, Kind: AccessIngress, Label: "Ingress " + in.Name, Port: b.Port.Number}})
	}
	if d := in.Spec.DefaultBackend; d != nil {
		add("", "/", d.Service)
	}
	for _, r := range in.Spec.Rules {
		if r.HTTP == nil {
			continue
		}
		for _, p := range r.HTTP.Paths {
			add(r.Host, p.Path, p.Backend.Service)
		}
	}
	return out
}

// HasLink reports whether u is one of the links of namespace/service.
func (a *Access) HasLink(namespace, service, u string) bool {
	for _, s := range a.Services {
		if s.Namespace != namespace || s.Name != service {
			continue
		}
		for _, l := range s.Links {
			if l.URL == u {
				return true
			}
		}
	}
	return false
}

// ProbeTimeout bounds one access test.
const ProbeTimeout = 5 * time.Second

// ProbeResult is the outcome of a quick reachability test.
type ProbeResult struct {
	URL       string `json:"url"`
	OK        bool   `json:"ok"`
	Status    int    `json:"status,omitempty"`
	LatencyMs int64  `json:"latencyMs"`
	Error     string `json:"error,omitempty"`
	// Insecure: the TLS certificate was not verified (self-signed is common).
	Insecure bool `json:"insecure,omitempty"`
}

// probeClient talks straight to the cluster network (no proxy from the
// environment), does not follow redirects and accepts self-signed
// certificates: the test is about reachability, nothing is sent but a GET.
var probeClient = &http.Client{
	Timeout: ProbeTimeout,
	Transport: &http.Transport{
		Proxy:                 nil,
		DialContext:           (&net.Dialer{Timeout: ProbeTimeout}).DialContext,
		TLSClientConfig:       &tls.Config{InsecureSkipVerify: true}, //nolint:gosec // reachability check of user apps, often self-signed
		TLSHandshakeTimeout:   ProbeTimeout,
		ResponseHeaderTimeout: ProbeTimeout,
		DisableKeepAlives:     true,
	},
	CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
}

// Probe GETs u once. Any HTTP answer (even 404 or 500) proves the path from
// k8sfront to the Service works; OK is true for status < 500.
func Probe(ctx context.Context, u string) ProbeResult {
	res := ProbeResult{URL: u}
	pu, err := url.Parse(u)
	if err != nil || (pu.Scheme != "http" && pu.Scheme != "https") {
		res.Error = "URL inválida"
		return res
	}
	res.Insecure = pu.Scheme == "https"
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		res.Error = err.Error()
		return res
	}
	req.Header.Set("User-Agent", "k8sfront-access-check")
	start := time.Now()
	resp, err := probeClient.Do(req)
	res.LatencyMs = time.Since(start).Milliseconds()
	if err != nil {
		res.Error = probeError(err)
		return res
	}
	resp.Body.Close()
	res.Status = resp.StatusCode
	res.OK = resp.StatusCode < 500
	if !res.OK {
		res.Error = fmt.Sprintf("o servidor respondeu %s", resp.Status)
	}
	return res
}

func probeError(err error) string {
	var ne net.Error
	switch {
	case errors.As(err, &ne) && ne.Timeout():
		return "sem resposta em 5s (firewall, IP fora da rede ou nenhum nó anunciando o IP)"
	case strings.Contains(err.Error(), "connection refused"):
		return "conexão recusada (nada escutando nessa porta)"
	case strings.Contains(err.Error(), "no route to host"), strings.Contains(err.Error(), "host is unreachable"):
		return "sem rota até o host (o IP não responde na rede)"
	}
	return err.Error()
}

package bootstrap

import (
	"fmt"
	"net"
	"strconv"
	"strings"
)

// Local API proxy (EndpointLocalProxy): every node, control planes and
// workers, runs Debian's HAProxy as the systemd unit k8sfront-apiproxy,
// listening on 127.0.0.1:LocalProxyPort and balancing to the kube-apiserver
// of every control plane with an HTTPS /readyz health check. LocalProxyHost
// resolves to 127.0.0.1 through /etc/hosts, so kubelet, kube-proxy, the CNI
// and kubeadm itself reach the API without a VIP or an external balancer.
//
// It is a host service rather than a static pod on purpose: a node that is
// joining has no running kubelet yet, but kubeadm join already needs the
// controlPlaneEndpoint for the TLS bootstrap.

// APIProxyConfig renders the HAProxy configuration for the given control
// planes. The output is deterministic so the script only reloads on change.
func APIProxyConfig(controlPlanes []Node) string {
	var b strings.Builder
	b.WriteString(`# Gerado pelo k8sfront: proxy local da API do Kubernetes. Não edite.
global
  maxconn 8000
  # Backend state changes land in the journal of k8sfront-apiproxy.service.
  log stderr format short local0 notice

defaults
  mode tcp
  log global
  option dontlognull
  timeout connect 5s
  # Watches are long-lived; clients reconnect on their own.
  timeout client 1h
  timeout server 1h

frontend kube-apiserver
  bind 127.0.0.1:` + strconv.Itoa(LocalProxyPort) + `
  default_backend control-planes

backend control-planes
  balance roundrobin
  option httpchk
  http-check send meth GET uri /readyz
  http-check expect status 200
  default-server check check-ssl verify none inter 2s fall 2 rise 2 on-marked-down shutdown-sessions
`)
	for i, n := range controlPlanes {
		b.WriteString(fmt.Sprintf("  server cp%d %s # %s\n", i+1, net.JoinHostPort(n.Host, strconv.Itoa(APIServerPort)), proxyComment(n.Name)))
	}
	return b.String()
}

// proxyComment keeps the server name in the config readable but inert.
func proxyComment(s string) string {
	return strings.Map(func(r rune) rune {
		if r < 0x20 || r == 0x7f {
			return -1
		}
		return r
	}, s)
}

// APIProxyScript installs (or updates) the local API proxy on a node with the
// given control planes as backends. Idempotent: HAProxy is reloaded only when
// the configuration changed.
func APIProxyScript(controlPlanes []Node) string {
	backends := make([]string, 0, len(controlPlanes))
	for _, n := range controlPlanes {
		backends = append(backends, net.JoinHostPort(n.Host, strconv.Itoa(APIServerPort)))
	}
	return render("apiproxy", map[string]any{
		"Config": APIProxyConfig(controlPlanes), "Host": LocalProxyHost, "Port": strconv.Itoa(LocalProxyPort),
		"Backends": strings.Join(backends, " "),
	})
}

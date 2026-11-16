package kube

import (
	"context"
	"errors"
	"fmt"
	"net"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/efbecker/k8sfront/apps/api/internal/bootstrap"
	"github.com/efbecker/k8sfront/apps/api/internal/store"
	"k8s.io/client-go/tools/clientcmd"
)

// apiEndpoints returns the control planes k8sfront dials for a cluster whose
// nodes reach the API through the local proxy (nil for every other mode,
// whose kubeconfig server is already reachable from outside).
func (m *Manager) apiEndpoints(ctx context.Context, c *store.Cluster) ([]string, error) {
	opts, err := bootstrap.ParseOptions(c.Options)
	if err != nil || opts.Mode() != bootstrap.EndpointLocalProxy {
		return nil, nil
	}
	return controlPlaneEndpoints(ctx, m.Store, c.ID)
}

func controlPlaneEndpoints(ctx context.Context, st store.Store, clusterID string) ([]string, error) {
	members, err := st.ListClusterServers(ctx, clusterID)
	if err != nil {
		return nil, err
	}
	var out []string
	for _, s := range members {
		if s.Role == store.RoleControlPlane {
			out = append(out, net.JoinHostPort(s.Host, strconv.Itoa(bootstrap.APIServerPort)))
		}
	}
	return out, nil
}

// DialTimeout bounds each attempt of the failover dialer.
const DialTimeout = 3 * time.Second

// failoverDialer connects to the first control plane that accepts a TCP
// connection, starting with the last one that worked.
type failoverDialer struct {
	addrs []string
	mu    sync.Mutex
	next  int
}

func (f *failoverDialer) DialContext(ctx context.Context, network, _ string) (net.Conn, error) {
	f.mu.Lock()
	start := f.next
	f.mu.Unlock()
	var errs []error
	d := net.Dialer{Timeout: DialTimeout, KeepAlive: 30 * time.Second}
	for i := range f.addrs {
		idx := (start + i) % len(f.addrs)
		conn, err := d.DialContext(ctx, network, f.addrs[idx])
		if err == nil {
			f.mu.Lock()
			f.next = idx
			f.mu.Unlock()
			return conn, nil
		}
		errs = append(errs, err)
		if ctx.Err() != nil {
			break
		}
	}
	return nil, fmt.Errorf("nenhum control plane respondeu: %w", errors.Join(errs...))
}

// ExternalKubeconfig adapts admin.conf for use outside the cluster. Nodes of
// a local-proxy cluster reach the API through LocalProxyHost on 127.0.0.1,
// which does not exist elsewhere: the server becomes the first control plane
// (keeping the endpoint name for TLS verification) and a header lists the
// others to switch to if it is down. Other modes are returned unchanged.
func (m *Manager) ExternalKubeconfig(ctx context.Context, c *store.Cluster, kc []byte) ([]byte, error) {
	endpoints, err := m.apiEndpoints(ctx, c)
	if err != nil || len(endpoints) == 0 {
		return kc, err
	}
	cfg, err := clientcmd.Load(kc)
	if err != nil {
		return nil, fmt.Errorf("kubeconfig inválido: %w", err)
	}
	for _, cl := range cfg.Clusters {
		cl.Server = "https://" + endpoints[0]
		cl.TLSServerName = bootstrap.LocalProxyHost
	}
	out, err := clientcmd.Write(*cfg)
	if err != nil {
		return nil, err
	}
	header := "# Cluster com proxy local da API: os nós usam " + bootstrap.LocalProxyHost + " (127.0.0.1). Fora do cluster,\n" +
		"# use qualquer control plane como server: " + strings.Join(endpoints, ", ") + "\n"
	return append([]byte(header), out...), nil
}

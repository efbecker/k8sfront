// Package kube talks to the Kubernetes API of managed clusters (phase 3).
//
// The admin kubeconfig is decrypted from the vault only to build an in-memory
// rest.Config; the plaintext bytes are wiped right after. Clients are cached
// per cluster and rebuilt whenever the kubeconfig secret changes.
//
// Everything returned to the browser goes through the slim views in views.go:
// raw objects are never forwarded (pod specs may carry literal env values, and
// Secrets are never read at all).
package kube

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"sync"
	"time"

	"github.com/efbecker/k8sfront/apps/api/internal/secrets"
	"github.com/efbecker/k8sfront/apps/api/internal/store"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/clientcmd"
)

// ErrNoKubeconfig means the cluster has not finished bootstrapping yet.
var ErrNoKubeconfig = errors.New("o cluster ainda não tem kubeconfig (o deploy não terminou)")

// RequestTimeout bounds every REST call made on behalf of an HTTP request.
const RequestTimeout = 15 * time.Second

// Manager owns one client (and, on demand, one live topology watcher) per cluster.
type Manager struct {
	Store   store.Store
	Secrets *secrets.Manager
	Log     *slog.Logger

	mu       sync.Mutex
	clusters map[string]*Cluster
}

// Cluster is a connected cluster.
type Cluster struct {
	ID        string
	Client    kubernetes.Interface
	secretID  string
	topoOnce  sync.Mutex
	topo      *Topology
	log       *slog.Logger
	closeOnce sync.Once
}

func NewManager(st store.Store, sec *secrets.Manager, log *slog.Logger) *Manager {
	return &Manager{Store: st, Secrets: sec, Log: log, clusters: map[string]*Cluster{}}
}

// Get returns the cached client for the cluster, building it from the vault
// if needed.
func (m *Manager) Get(ctx context.Context, clusterID string) (*Cluster, error) {
	c, err := m.Store.GetCluster(ctx, clusterID)
	if err != nil {
		return nil, err
	}
	if c.KubeconfigSecretID == nil {
		return nil, ErrNoKubeconfig
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if cur, ok := m.clusters[clusterID]; ok {
		if cur.secretID == *c.KubeconfigSecretID {
			return cur, nil
		}
		cur.close()
		delete(m.clusters, clusterID)
	}
	kc, err := m.Secrets.Get(ctx, *c.KubeconfigSecretID)
	if err != nil {
		return nil, err
	}
	cfg, err := clientcmd.RESTConfigFromKubeConfig(kc)
	for i := range kc {
		kc[i] = 0
	}
	if err != nil {
		return nil, fmt.Errorf("kubeconfig inválido: %w", err)
	}
	cs, err := newClientset(cfg)
	if err != nil {
		return nil, err
	}
	cl := &Cluster{ID: clusterID, Client: cs, secretID: *c.KubeconfigSecretID, log: m.Log.With("cluster", clusterID)}
	m.clusters[clusterID] = cl
	return cl, nil
}

// Forget drops the cached client (e.g. when the cluster is removed).
func (m *Manager) Forget(clusterID string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if cur, ok := m.clusters[clusterID]; ok {
		cur.close()
		delete(m.clusters, clusterID)
	}
}

// Close stops every watcher.
func (m *Manager) Close() {
	m.mu.Lock()
	defer m.mu.Unlock()
	for id, c := range m.clusters {
		c.close()
		delete(m.clusters, id)
	}
}

func newClientset(cfg *rest.Config) (*kubernetes.Clientset, error) {
	cfg = rest.CopyConfig(cfg)
	cfg.UserAgent = "k8sfront"
	cfg.Timeout = 0 // watches are long-lived; per-request deadlines come from contexts
	cfg.QPS = 20
	cfg.Burst = 40
	return kubernetes.NewForConfig(cfg)
}

// Topology returns the live watcher, starting it on first use.
func (c *Cluster) Topology() *Topology {
	c.topoOnce.Lock()
	defer c.topoOnce.Unlock()
	if c.topo == nil {
		c.topo = NewTopology(c.Client, c.log)
	}
	return c.topo
}

func (c *Cluster) close() {
	c.closeOnce.Do(func() {
		c.topoOnce.Lock()
		defer c.topoOnce.Unlock()
		if c.topo != nil {
			c.topo.Stop()
		}
	})
}

// Inject registers a client for clusterID as if it had been built from the
// kubeconfig secret secretID (used by tests with a fake clientset).
func (m *Manager) Inject(clusterID, secretID string, cs kubernetes.Interface) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.clusters[clusterID] = &Cluster{ID: clusterID, Client: cs, secretID: secretID, log: m.Log.With("cluster", clusterID)}
}

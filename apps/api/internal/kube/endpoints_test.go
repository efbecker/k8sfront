package kube

import (
	"context"
	"io"
	"log/slog"
	"net"
	"path/filepath"
	"strings"
	"testing"

	"github.com/efbecker/k8sfront/apps/api/internal/bootstrap"
	"github.com/efbecker/k8sfront/apps/api/internal/store"
	"github.com/efbecker/k8sfront/apps/api/internal/store/sqlite"
)

func TestFailoverDialerSkipsDeadControlPlanes(t *testing.T) {
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer l.Close()
	go func() {
		for {
			c, err := l.Accept()
			if err != nil {
				return
			}
			c.Close()
		}
	}()
	dead, _ := net.Listen("tcp", "127.0.0.1:0")
	deadAddr := dead.Addr().String()
	dead.Close()
	d := &failoverDialer{addrs: []string{deadAddr, l.Addr().String()}}
	for i := 0; i < 2; i++ {
		c, err := d.DialContext(context.Background(), "tcp", bootstrap.LocalProxyHost+":7445")
		if err != nil {
			t.Fatal(err)
		}
		if c.RemoteAddr().String() != l.Addr().String() {
			t.Fatalf("dialed %s", c.RemoteAddr())
		}
		c.Close()
	}
	if d.next != 1 {
		t.Fatal("the dialer must remember the control plane that answered")
	}
	all := &failoverDialer{addrs: []string{deadAddr}}
	if _, err := all.DialContext(context.Background(), "tcp", "x:1"); err == nil || !strings.Contains(err.Error(), "nenhum control plane") {
		t.Fatalf("expected failure, got %v", err)
	}
}

func TestExternalKubeconfigPointsAtControlPlanes(t *testing.T) {
	ctx := context.Background()
	db, err := sqlite.Open(ctx, filepath.Join(t.TempDir(), "db.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	cl := &store.Cluster{ID: "c1", Name: "prod", KubernetesVersion: "v1.37.2", PodCIDR: "10.244.0.0/16", ServiceCIDR: "10.96.0.0/12",
		CNI: "cilium", Topology: "multi-node", Options: []byte(`{"endpointMode":"local-proxy"}`)}
	if err := db.CreateCluster(ctx, cl); err != nil {
		t.Fatal(err)
	}
	for i, h := range []string{"10.0.0.11", "10.0.0.12", "10.0.0.13"} {
		cid := "c1"
		db.CreateServer(ctx, &store.Server{ID: h, ClusterID: &cid, Name: "cp" + h[len(h)-1:], Host: h, Port: 22 + i, SSHUser: "root",
			AuthMethod: store.AuthPassword, Role: store.RoleControlPlane})
	}
	m := NewManager(db, nil, slog.New(slog.NewTextHandler(io.Discard, nil)))
	kc := []byte("apiVersion: v1\nkind: Config\nclusters:\n- name: prod\n  cluster:\n    server: https://k8s-api.k8sfront.internal:7445\n" +
		"contexts:\n- name: admin@prod\n  context:\n    cluster: prod\n    user: admin\ncurrent-context: admin@prod\nusers:\n- name: admin\n  user:\n    token: x\n")
	out, err := m.ExternalKubeconfig(ctx, cl, kc)
	if err != nil {
		t.Fatal(err)
	}
	s := string(out)
	if !strings.Contains(s, "server: https://10.0.0.11:6443") || !strings.Contains(s, "tls-server-name: "+bootstrap.LocalProxyHost) ||
		!strings.Contains(s, "10.0.0.13:6443") {
		t.Fatalf("unexpected kubeconfig:\n%s", s)
	}
	cl.Options = []byte(`{"kubeVip":{"address":"10.0.0.50"}}`)
	if out, _ := m.ExternalKubeconfig(ctx, cl, kc); string(out) != string(kc) {
		t.Fatal("kube-vip kubeconfigs are reachable as they are")
	}
}

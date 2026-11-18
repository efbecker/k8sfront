package api

import (
	"context"
	"testing"

	"github.com/efbecker/k8sfront/apps/api/internal/kube"
	"github.com/efbecker/k8sfront/apps/api/internal/store"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes/fake"
)

func TestKubeEndpoints(t *testing.T) {
	ts, db, s := newTestServerFull(t)
	ctx := context.Background()
	c := &store.Cluster{ID: "c1", Name: "lab", KubernetesVersion: "v1.37.2", PodCIDR: "10.244.0.0/16", ServiceCIDR: "10.96.0.0/12",
		CNI: "cilium", Topology: "single-node", Status: store.ClusterReady, Options: []byte("{}")}
	if err := db.CreateCluster(ctx, c); err != nil {
		t.Fatal(err)
	}
	var e apiError
	if code := call(t, ts, "GET", "/api/v1/clusters/c1/k8s/overview", nil, &e); code != 409 || e.Error != "not_ready" {
		t.Fatalf("without kubeconfig: %d %+v", code, e)
	}
	if code := call(t, ts, "GET", "/api/v1/clusters/nope/k8s/overview", nil, nil); code != 404 {
		t.Fatalf("unknown cluster: %d", code)
	}

	secretID, err := s.Secrets.Put(ctx, store.SecretKubeconfig, []byte("not used by the fake"))
	if err != nil {
		t.Fatal(err)
	}
	c.KubeconfigSecretID = &secretID
	if err := db.UpdateCluster(ctx, c); err != nil {
		t.Fatal(err)
	}
	s.Kube.Inject("c1", secretID, fake.NewClientset(
		&corev1.Node{ObjectMeta: metav1.ObjectMeta{Name: "n1", UID: "n1"}},
		&corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: "default"}},
		&corev1.Service{ObjectMeta: metav1.ObjectMeta{Namespace: "default", Name: "kubernetes"}, Spec: corev1.ServiceSpec{ClusterIP: "10.96.0.1",
			Ports: []corev1.ServicePort{{Port: 443, Protocol: corev1.ProtocolTCP}}}},
	))
	var ov kube.Overview
	if code := call(t, ts, "GET", "/api/v1/clusters/c1/k8s/overview", nil, &ov); code != 200 || len(ov.Nodes) != 1 || ov.Services != 1 {
		t.Fatalf("overview: %d %+v", code, ov)
	}
	var ns []string
	if code := call(t, ts, "GET", "/api/v1/clusters/c1/k8s/namespaces", nil, &ns); code != 200 || len(ns) != 1 {
		t.Fatalf("namespaces: %d %v", code, ns)
	}
	var net kube.Network
	if code := call(t, ts, "GET", "/api/v1/clusters/c1/k8s/network?namespace=default", nil, &net); code != 200 || len(net.Services) != 1 || net.Services[0].Ports[0] != "443/TCP" {
		t.Fatalf("network: %d %+v", code, net)
	}
	if code := call(t, ts, "GET", "/api/v1/clusters/c1/k8s/workloads?namespace=Bad_NS", nil, nil); code != 400 {
		t.Fatalf("bad namespace: %d", code)
	}
	var wl kube.Workloads
	if code := call(t, ts, "GET", "/api/v1/clusters/c1/k8s/workloads", nil, &wl); code != 200 {
		t.Fatalf("workloads: %d", code)
	}
	var acc kube.Access
	if code := call(t, ts, "GET", "/api/v1/clusters/c1/k8s/access", nil, &acc); code != 200 || len(acc.Services) != 0 {
		t.Fatalf("access (ClusterIP only): %d %+v", code, acc)
	}
	if code := call(t, ts, "GET", "/api/v1/clusters/c1/k8s/access/probe?namespace=default&service=kubernetes&url=http://169.254.169.254/", nil, &e); code != 400 {
		t.Fatalf("probe of a URL that is not a Service link must be refused: %d %+v", code, e)
	}
	if code := call(t, ts, "POST", "/api/v1/clusters/c1/loadbalancer", map[string]any{"addresses": []string{"10.244.0.10-10.244.0.20"}}, &e); code != 400 || e.Error != "validation" {
		t.Fatalf("loadbalancer inside the pod CIDR: %d %+v", code, e)
	}
}

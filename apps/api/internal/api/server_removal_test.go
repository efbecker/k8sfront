package api

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/efbecker/k8sfront/apps/api/internal/ops"
	"github.com/efbecker/k8sfront/apps/api/internal/store"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

func TestServerRemoval(t *testing.T) {
	ts, db, s := newTestServerFull(t)
	cs := readyClusterFixture(t, db, s)
	ctx := context.Background()
	// Give w1 its InternalIP and some pods.
	n, _ := cs.CoreV1().Nodes().Get(ctx, "w1", metav1.GetOptions{})
	n.Status.Addresses = []corev1.NodeAddress{{Type: corev1.NodeInternalIP, Address: "10.0.0.2"}}
	cs.CoreV1().Nodes().UpdateStatus(ctx, n, metav1.UpdateOptions{})
	ctrl := true
	pod := func(name, ownerKind string) *corev1.Pod {
		p := &corev1.Pod{ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: "app"}, Spec: corev1.PodSpec{NodeName: "w1"}}
		if ownerKind != "" {
			p.OwnerReferences = []metav1.OwnerReference{{Kind: ownerKind, Name: "x", Controller: &ctrl}}
		}
		return p
	}
	for _, p := range []*corev1.Pod{pod("web", "ReplicaSet"), pod("agent", "DaemonSet"), pod("naked", "")} {
		cs.CoreV1().Pods("app").Create(ctx, p, metav1.CreateOptions{})
	}

	var plan serverRemoval
	if code := call(t, ts, "GET", "/api/v1/servers/cp1/removal", nil, &plan); code != 200 || plan.Blocked == "" {
		t.Fatalf("control plane must be blocked: %d %+v", code, plan)
	}
	var e apiError
	if code := call(t, ts, "POST", "/api/v1/servers/cp1/remove", nil, &e); code != 409 {
		t.Fatalf("remove control plane: %d", code)
	}
	if code := call(t, ts, "GET", "/api/v1/servers/w1/removal", nil, &plan); code != 200 || plan.Node == nil || plan.Node.NodeName != "w1" || len(plan.Node.Pods) != 3 {
		t.Fatalf("worker plan: %d %+v", code, plan)
	}
	fates := map[string]string{}
	for _, p := range plan.Node.Pods {
		fates[p.Name] = p.Fate
	}
	if fates["web"] != "moves" || fates["agent"] != "daemon" || fates["naked"] != "blocks" || len(plan.Warnings) == 0 {
		t.Fatalf("fates %v warnings %v", fates, plan.Warnings)
	}
	var job store.Job
	if code := call(t, ts, "POST", "/api/v1/servers/w1/remove", nil, &job); code != 202 || job.Kind != ops.KindNodeRemove {
		t.Fatalf("remove worker: %d %+v", code, job)
	}
	var p ops.NodeParams
	if err := json.Unmarshal(job.Params, &p); err != nil || !p.DeleteServer || p.ServerID != "w1" {
		t.Fatalf("params %s", job.Params)
	}
	s.Engine.Cancel(ctx, job.ID)

	if code := call(t, ts, "GET", "/api/v1/servers/free/removal", nil, &plan); code != 200 || plan.InCluster {
		t.Fatalf("free plan: %+v", plan)
	}
	if code := call(t, ts, "POST", "/api/v1/servers/free/remove", nil, nil); code != 200 {
		t.Fatalf("remove free: %d", code)
	}
	if _, err := db.GetServer(ctx, "free"); err == nil {
		t.Fatal("free server still in inventory")
	}
}

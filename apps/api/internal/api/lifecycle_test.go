package api

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/efbecker/k8sfront/apps/api/internal/k8sver"
	"github.com/efbecker/k8sfront/apps/api/internal/ops"
	"github.com/efbecker/k8sfront/apps/api/internal/store"
	"github.com/efbecker/k8sfront/apps/api/internal/store/sqlite"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/version"
	fakediscovery "k8s.io/client-go/discovery/fake"
	"k8s.io/client-go/kubernetes/fake"
)

func readyClusterFixture(t *testing.T, db *sqlite.DB, s *Server) *fake.Clientset {
	t.Helper()
	ctx := context.Background()
	secretID, _ := s.Secrets.Put(ctx, store.SecretKubeconfig, []byte("unused"))
	c := &store.Cluster{ID: "c1", Name: "lab", KubernetesVersion: "v1.37.2", PodCIDR: "10.244.0.0/16", ServiceCIDR: "10.96.0.0/12",
		CNI: "cilium", Topology: "multi-node", Status: store.ClusterReady, Options: []byte("{}"), KubeconfigSecretID: &secretID}
	if err := db.CreateCluster(ctx, c); err != nil {
		t.Fatal(err)
	}
	cid := "c1"
	for _, sv := range []store.Server{
		{ID: "cp1", ClusterID: &cid, Name: "cp1", Host: "10.0.0.1", Port: 22, SSHUser: "root", AuthMethod: store.AuthPassword, Role: store.RoleControlPlane},
		{ID: "w1", ClusterID: &cid, Name: "w1", Host: "10.0.0.2", Port: 22, SSHUser: "root", AuthMethod: store.AuthPassword, Role: store.RoleWorker},
		{ID: "free", Name: "free", Host: "10.0.0.3", Port: 22, SSHUser: "root", AuthMethod: store.AuthPassword},
	} {
		sv := sv
		if err := db.CreateServer(ctx, &sv); err != nil {
			t.Fatal(err)
		}
	}
	ready := corev1.NodeCondition{Type: corev1.NodeReady, Status: corev1.ConditionTrue}
	cs := fake.NewClientset(
		&corev1.Node{ObjectMeta: metav1.ObjectMeta{Name: "cp1", Labels: map[string]string{"node-role.kubernetes.io/control-plane": ""}},
			Status: corev1.NodeStatus{Conditions: []corev1.NodeCondition{ready}, NodeInfo: corev1.NodeSystemInfo{KubeletVersion: "v1.37.2"}}},
		&corev1.Node{ObjectMeta: metav1.ObjectMeta{Name: "w1"}, Status: corev1.NodeStatus{Conditions: []corev1.NodeCondition{ready}, NodeInfo: corev1.NodeSystemInfo{KubeletVersion: "v1.37.2"}}},
	)
	cs.Discovery().(*fakediscovery.FakeDiscovery).FakedServerVersion = &version.Info{GitVersion: "v1.37.2"}
	s.Kube.Inject("c1", secretID, cs)
	return cs
}

func TestUpgradeAndLifecycleAPI(t *testing.T) {
	ts, db, s := newTestServerFull(t)
	markers := map[string]string{"/stable.txt": "v1.39.0", "/stable-1.39.txt": "v1.39.0", "/stable-1.38.txt": "v1.38.4", "/stable-1.37.txt": "v1.37.5", "/stable-1.36.txt": "v1.36.9"}
	dl := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if m, ok := markers[r.URL.Path]; ok {
			w.Write([]byte(m))
			return
		}
		http.NotFound(w, r)
	}))
	defer dl.Close()
	f := k8sver.NewFetcher()
	f.BaseURL = dl.URL
	s.Versions = f
	cs := readyClusterFixture(t, db, s)

	var plan upgradePlan
	if code := call(t, ts, "GET", "/api/v1/clusters/c1/upgrades/plan", nil, &plan); code != 200 || plan.Blocked || len(plan.Targets) != 3 {
		t.Fatalf("plan: %d %+v", code, plan)
	}
	if last := plan.Targets[2]; last.Version != "v1.39.0" || strings.Join(last.Hops, ",") != "v1.38.4,v1.39.0" {
		t.Fatalf("chain: %+v", last)
	}
	var e apiError
	if code := call(t, ts, "POST", "/api/v1/clusters/c1/upgrades", map[string]string{"targetVersion": "v1.40.0"}, &e); code != 400 {
		t.Fatalf("unknown target: %d %+v", code, e)
	}
	var job store.Job
	if code := call(t, ts, "POST", "/api/v1/clusters/c1/upgrades", map[string]string{"targetVersion": "v1.39.0"}, &job); code != 202 || job.Kind != ops.KindUpgrade {
		t.Fatalf("start: %d %+v", code, job)
	}
	if !strings.Contains(string(job.Params), `"hops":["v1.38.4","v1.39.0"]`) {
		t.Fatalf("params %s", job.Params)
	}
	// Exclusive maintenance: a second one is refused while the first is pending.
	if code := call(t, ts, "POST", "/api/v1/clusters/c1/rolling-reboot", map[string]any{}, &e); code != 409 {
		t.Fatalf("concurrent maintenance: %d %+v", code, e)
	}
	if code := call(t, ts, "GET", "/api/v1/clusters/c1/upgrades/plan", nil, &plan); code != 200 || !plan.Blocked || plan.PausedJob == nil {
		t.Fatalf("plan with pending upgrade must be blocked: %+v", plan)
	}
	s.Engine.Cancel(context.Background(), job.ID)

	if code := call(t, ts, "DELETE", "/api/v1/clusters/c1/nodes/cp1", nil, &e); code != 400 {
		t.Fatalf("removing a control plane: %d", code)
	}
	if code := call(t, ts, "POST", "/api/v1/clusters/c1/nodes", map[string]string{"serverId": "free"}, &job); code != 202 || job.Kind != ops.KindNodeAdd {
		t.Fatalf("add node: %d %+v", code, job)
	}
	if srv, _ := db.GetServer(context.Background(), "free"); srv.ClusterID == nil || srv.Role != store.RoleWorker {
		t.Fatalf("server not attached as worker: %+v", srv)
	}
	s.Engine.Cancel(context.Background(), job.ID)

	if code := call(t, ts, "POST", "/api/v1/clusters/c1/k8s/nodes/w1/cordon", nil, nil); code != 204 {
		t.Fatalf("cordon: %d", code)
	}
	if n, _ := cs.CoreV1().Nodes().Get(context.Background(), "w1", metav1.GetOptions{}); !n.Spec.Unschedulable {
		t.Fatal("node not cordoned")
	}
	if code := call(t, ts, "POST", "/api/v1/clusters/c1/k8s/nodes/Bad_Name/cordon", nil, nil); code != 400 {
		t.Fatalf("bad node name: %d", code)
	}
	if code := call(t, ts, "POST", "/api/v1/clusters/c1/os-updates", map[string]any{"reboot": "sometimes"}, nil); code != 400 {
		t.Fatalf("bad reboot policy: %d", code)
	}
	if code := call(t, ts, "POST", "/api/v1/clusters/c1/os-updates", map[string]any{"serverIds": []string{"free-not-member"}}, nil); code != 400 {
		t.Fatalf("non-member server: %d", code)
	}
}

func TestDeployAPI(t *testing.T) {
	ts, db, s := newTestServerFull(t)
	readyClusterFixture(t, db, s)
	var rr renderResponse
	app := map[string]any{"name": "web", "namespace": "shop", "image": "nginx:1.29", "replicas": 2, "containerPort": 80,
		"service": map[string]any{"type": "ClusterIP"}, "pullSecret": map[string]any{"server": "reg.example.com", "username": "u", "password": "hunter2-secret"}}
	if code := call(t, ts, "POST", "/api/v1/deploy/render", app, &rr); code != 200 || len(rr.Objects) != 3 || strings.Contains(rr.YAML, "hunter2") {
		t.Fatalf("render: %d %+v", code, rr)
	}
	var e apiError
	if code := call(t, ts, "POST", "/api/v1/deploy/render", map[string]any{"name": "Bad", "image": "x"}, &e); code != 400 || e.Error != "validation" {
		t.Fatalf("invalid app: %d %+v", code, e)
	}
	if code := call(t, ts, "POST", "/api/v1/clusters/c1/apply", map[string]any{"manifest": "kind: x"}, &e); code != 400 {
		t.Fatalf("invalid manifest: %d", code)
	}
	if code := call(t, ts, "POST", "/api/v1/clusters/c1/delete", map[string]any{"objects": []any{}}, &e); code != 400 || e.Error != "validation" {
		t.Fatalf("empty delete: %d %+v", code, e)
	}

	var job store.Job
	in := map[string]any{"release": "redis", "namespace": "cache", "chart": "redis", "repo": "https://charts.example.com", "values": "auth:\n  password: topsecret\n"}
	if code := call(t, ts, "POST", "/api/v1/clusters/c1/helm", in, &job); code != 202 || job.Kind != ops.KindHelm {
		t.Fatalf("helm: %d %+v", code, job)
	}
	if strings.Contains(string(job.Params), "topsecret") || !strings.Contains(string(job.Params), "valuesSecretId") {
		t.Fatalf("values must go to the vault, not the params: %s", job.Params)
	}
	if code := call(t, ts, "POST", "/api/v1/clusters/c1/helm", map[string]any{"release": "x", "chart": "a", "repo": "ftp://x"}, &e); code != 400 {
		t.Fatalf("bad repo: %d", code)
	}
	var rels []any
	if code := call(t, ts, "GET", "/api/v1/clusters/c1/helm/releases", nil, &rels); code != 200 {
		t.Fatalf("releases: %d", code)
	}
}

package api

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"
	"time"

	"github.com/efbecker/k8sfront/apps/api/internal/jobs"
	"github.com/efbecker/k8sfront/apps/api/internal/ops"
	"github.com/efbecker/k8sfront/apps/api/internal/secrets"
	"github.com/efbecker/k8sfront/apps/api/internal/store"
	"github.com/efbecker/k8sfront/apps/api/internal/store/sqlite"
	"github.com/efbecker/k8sfront/apps/api/internal/vault"
)

const testToken = "unit-test-admin-token"

func newTestServer(t *testing.T) (*httptest.Server, *sqlite.DB) {
	t.Helper()
	db, err := sqlite.Open(context.Background(), filepath.Join(t.TempDir(), "api.db"))
	if err != nil {
		t.Fatal(err)
	}
	key := make([]byte, 32)
	rand.Read(key)
	v, _ := vault.New(key)
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	// The engine is not started: submitted jobs stay pending, which is what
	// these API-level tests need.
	engine := jobs.New(db, jobs.NewHub(db, logger), logger, 1)
	(&ops.Ops{Store: db}).Register(engine)
	s := &Server{Store: db, Secrets: secrets.NewManager(db, v), Engine: engine, Log: logger, Auth: NewAuth(testToken, key, time.Hour, false)}
	ts := httptest.NewServer(s.Handler())
	t.Cleanup(func() { ts.Close(); db.Close() })
	return ts, db
}

func call(t *testing.T, ts *httptest.Server, method, path string, body any, out any) int {
	t.Helper()
	var rd io.Reader
	if body != nil {
		b, _ := json.Marshal(body)
		rd = bytes.NewReader(b)
	}
	req, _ := http.NewRequest(method, ts.URL+path, rd)
	req.Header.Set("Authorization", "Bearer "+testToken)
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if out != nil {
		json.NewDecoder(resp.Body).Decode(out)
	}
	return resp.StatusCode
}

func TestClusterLifecycleAPI(t *testing.T) {
	ts, _ := newTestServer(t)
	ids := map[string]string{}
	for _, h := range []string{"10.0.0.11", "10.0.0.12", "10.0.0.21"} {
		var srv struct{ ID string }
		if code := call(t, ts, "POST", "/api/v1/servers", map[string]any{"host": h, "sshUser": "root", "authMethod": "password", "password": "pw-123456"}, &srv); code != 201 {
			t.Fatalf("create server: %d", code)
		}
		ids[h] = srv.ID
	}
	base := map[string]any{"name": "lab", "kubernetesVersion": "v1.37.2", "podCidr": "10.244.0.0/16", "serviceCidr": "10.96.0.0/12",
		"cni": "cilium", "topology": "multi-node"}

	// Two control planes without a VIP is rejected before anything is stored.
	bad := map[string]any{}
	for k, v := range base {
		bad[k] = v
	}
	bad["nodes"] = []map[string]string{{"serverId": ids["10.0.0.11"], "role": "control-plane"}, {"serverId": ids["10.0.0.12"], "role": "control-plane"}}
	var apiErr apiError
	if code := call(t, ts, "POST", "/api/v1/clusters", bad, &apiErr); code != 400 {
		t.Fatalf("invalid HA spec: %d %+v", code, apiErr)
	}

	good := base
	good["nodes"] = []map[string]string{{"serverId": ids["10.0.0.11"], "role": "control-plane"}, {"serverId": ids["10.0.0.21"], "role": "worker"}}
	good["start"] = true
	var cl clusterView
	if code := call(t, ts, "POST", "/api/v1/clusters", good, &cl); code != 201 {
		t.Fatalf("create cluster: %d", code)
	}
	if len(cl.Nodes) != 2 || cl.Nodes[0].Role != store.RoleControlPlane || cl.BootstrapJob == nil || cl.BootstrapJob.Status != store.JobPending {
		t.Fatalf("unexpected cluster view: %+v", cl)
	}

	// Servers cannot join two clusters.
	other := map[string]any{"name": "lab2", "kubernetesVersion": "v1.37.2", "podCidr": "10.244.0.0/16", "serviceCidr": "10.96.0.0/12",
		"cni": "calico", "topology": "single-node", "nodes": []map[string]string{{"serverId": ids["10.0.0.11"], "role": "control-plane"}}}
	if code := call(t, ts, "POST", "/api/v1/clusters", other, nil); code != 409 {
		t.Fatalf("server reuse: %d", code)
	}
	// A bootstrap already queued cannot be started twice, and blocks deletion.
	if code := call(t, ts, "POST", "/api/v1/clusters/"+cl.ID+"/bootstrap", nil, nil); code != 409 {
		t.Fatalf("double bootstrap: %d", code)
	}
	if code := call(t, ts, "DELETE", "/api/v1/clusters/"+cl.ID, nil, nil); code != 409 {
		t.Fatalf("delete with pending job: %d", code)
	}
	if code := call(t, ts, "POST", "/api/v1/jobs/"+cl.BootstrapJob.ID+"/cancel", nil, nil); code != 202 {
		t.Fatalf("cancel: %d", code)
	}
	// Starting again resumes the same job instead of creating a new one.
	var job store.Job
	if code := call(t, ts, "POST", "/api/v1/clusters/"+cl.ID+"/bootstrap", nil, &job); code != 202 || job.ID != cl.BootstrapJob.ID {
		t.Fatalf("resume: %d %s", code, job.ID)
	}
	call(t, ts, "POST", "/api/v1/jobs/"+job.ID+"/cancel", nil, nil)
	if code := call(t, ts, "GET", "/api/v1/clusters/"+cl.ID+"/kubeconfig", nil, nil); code != 404 {
		t.Fatalf("kubeconfig before bootstrap: %d", code)
	}
	if code := call(t, ts, "DELETE", "/api/v1/clusters/"+cl.ID, nil, nil); code != 204 {
		t.Fatalf("delete: %d", code)
	}
	var srv serverView
	call(t, ts, "GET", "/api/v1/servers/"+ids["10.0.0.11"], nil, &srv)
	if srv.ClusterID != nil {
		t.Fatal("servers must be detached when the cluster is removed")
	}
}

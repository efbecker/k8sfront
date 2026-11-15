package api

import (
	"context"
	"testing"
	"time"
)

func TestServerEditResetsCheckAndDeleteGuard(t *testing.T) {
	ts, db := newTestServer(t)
	ctx := context.Background()
	var srv serverView
	if code := call(t, ts, "POST", "/api/v1/servers", map[string]any{"name": "no2", "host": "10.0.0.11", "sshUser": "root", "authMethod": "password", "password": "pw-123456"}, &srv); code != 201 {
		t.Fatalf("create: %d", code)
	}
	markChecked := func() {
		t.Helper()
		s, err := db.GetServer(ctx, srv.ID)
		if err != nil {
			t.Fatal(err)
		}
		now := time.Now()
		s.LastCheckAt, s.LastCheckStatus = &now, "fail"
		if err := db.UpdateServer(ctx, s); err != nil {
			t.Fatal(err)
		}
	}

	// Renaming alone keeps the pre-flight result.
	markChecked()
	var got serverView
	if code := call(t, ts, "PATCH", "/api/v1/servers/"+srv.ID, map[string]any{"name": "cp-01", "host": "10.0.0.11", "sshUser": "root", "authMethod": "password"}, &got); code != 200 {
		t.Fatalf("rename: %d", code)
	}
	if got.Name != "cp-01" || got.LastCheckStatus != "fail" {
		t.Fatalf("rename changed check state: %+v", got)
	}

	// Changing how we connect clears it so the server must be tested again.
	if code := call(t, ts, "PATCH", "/api/v1/servers/"+srv.ID, map[string]any{"host": "10.0.0.12", "sshUser": "debian", "authMethod": "password"}, &got); code != 200 {
		t.Fatalf("edit host: %d", code)
	}
	if got.Host != "10.0.0.12" || got.SSHUser != "debian" || got.LastCheckStatus != "" || got.LastCheckAt != nil {
		t.Fatalf("edit did not reset check: %+v", got)
	}

	// A server that belongs to a cluster cannot be deleted.
	var cl clusterView
	if code := call(t, ts, "POST", "/api/v1/clusters", map[string]any{"name": "lab", "kubernetesVersion": "v1.37.2", "podCidr": "10.244.0.0/16",
		"serviceCidr": "10.96.0.0/12", "cni": "cilium", "topology": "single-node",
		"nodes": []map[string]string{{"serverId": srv.ID, "role": "control-plane"}}}, &cl); code != 201 {
		t.Fatalf("create cluster: %d", code)
	}
	if code := call(t, ts, "DELETE", "/api/v1/servers/"+srv.ID, nil, nil); code != 409 {
		t.Fatalf("delete clustered server: %d", code)
	}
	if code := call(t, ts, "DELETE", "/api/v1/clusters/"+cl.ID, nil, nil); code != 204 {
		t.Fatalf("delete cluster: %d", code)
	}
	if code := call(t, ts, "DELETE", "/api/v1/servers/"+srv.ID, nil, nil); code != 204 {
		t.Fatalf("delete: %d", code)
	}
	if code := call(t, ts, "GET", "/api/v1/servers/"+srv.ID, nil, nil); code != 404 {
		t.Fatalf("get after delete: %d", code)
	}
}

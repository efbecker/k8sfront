package api

import (
	"context"
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/efbecker/k8sfront/apps/api/internal/store"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes/fake"
)

func TestRegistriesAPI(t *testing.T) {
	ts, db, s := newTestServerFull(t)
	ctx := context.Background()

	var e apiError
	if code := call(t, ts, "POST", "/api/v1/registries", map[string]any{"name": "x", "kind": "generic", "host": "bad host"}, &e); code != 400 || e.Error != "validation" {
		t.Fatalf("invalid host: %d %+v", code, e)
	}
	if code := call(t, ts, "POST", "/api/v1/registries", map[string]any{"name": "x", "kind": "ghcr", "password": "p"}, &e); code != 400 {
		t.Fatalf("password without user: %d", code)
	}
	var reg registryView
	if code := call(t, ts, "POST", "/api/v1/registries", map[string]any{"name": "ghcr", "kind": "ghcr", "username": "ci", "password": "sup3r-secret-token",
		"namespaces": []string{"apps"}}, &reg); code != 201 {
		t.Fatalf("create: %d", code)
	}
	if reg.Host != "ghcr.io" || !reg.HasPassword || !reg.PullSecret || reg.SecretName != "k8sfront-registry-ghcr" {
		t.Fatalf("view: %+v", reg)
	}
	if code := call(t, ts, "POST", "/api/v1/registries", map[string]any{"name": "ghcr2", "kind": "ghcr"}, &e); code != 409 {
		t.Fatalf("duplicate host must conflict: %d", code)
	}
	// The password never comes back, in any endpoint.
	for _, path := range []string{"/api/v1/registries", "/api/v1/registries/" + reg.ID, "/api/v1/audit"} {
		req, _ := http.NewRequest("GET", ts.URL+path, nil)
		req.Header.Set("Authorization", "Bearer "+testToken)
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		b, _ := io.ReadAll(resp.Body)
		resp.Body.Close()
		if strings.Contains(string(b), "sup3r-secret-token") {
			t.Fatalf("%s leaks the password", path)
		}
	}
	if code := call(t, ts, "PATCH", "/api/v1/registries/"+reg.ID, map[string]any{"mirrors": []string{"https://cache.local:5000"}}, &reg); code != 200 ||
		!reg.HasPassword || len(reg.Mirrors) != 1 {
		t.Fatalf("patch keeps the password: %d %+v", code, reg)
	}
	oldSecret := ""
	if g, _ := db.GetRegistry(ctx, reg.ID); g.PasswordSecretID != nil {
		oldSecret = *g.PasswordSecretID
	}
	if code := call(t, ts, "PATCH", "/api/v1/registries/"+reg.ID, map[string]any{"password": "new-token"}, &reg); code != 200 {
		t.Fatalf("rotate: %d", code)
	}
	if _, err := db.GetSecret(ctx, oldSecret); err == nil {
		t.Fatal("rotating the password must delete the old secret")
	}
	if code := call(t, ts, "PATCH", "/api/v1/registries/"+reg.ID, map[string]any{"clusterIds": []string{"nope"}}, &e); code != 400 {
		t.Fatalf("unknown cluster: %d", code)
	}

	// Deploy center: a form referencing the registry renders its shared Secret name.
	var rendered renderResponse
	app := map[string]any{"name": "web", "namespace": "apps", "image": "ghcr.io/org/web:1", "replicas": 1, "containerPort": 80,
		"resources": map[string]string{}, "pullSecret": map[string]string{"registryId": reg.ID}}
	if code := call(t, ts, "POST", "/api/v1/deploy/render", app, &rendered); code != 200 || !strings.Contains(rendered.YAML, "name: k8sfront-registry-ghcr") {
		t.Fatalf("render with registry: %d\n%s", code, rendered.YAML)
	}
	// Matching image host selects the registry automatically.
	delete(app, "pullSecret")
	if code := call(t, ts, "POST", "/api/v1/deploy/render", app, &rendered); code != 200 || !strings.Contains(rendered.YAML, "k8sfront-registry-ghcr") {
		t.Fatalf("auto registry: %d\n%s", code, rendered.YAML)
	}
	if strings.Contains(rendered.YAML, "new-token") {
		t.Fatal("preview leaks the password")
	}

	// Cluster endpoints.
	c := &store.Cluster{ID: "c1", Name: "lab", KubernetesVersion: "v1.37.2", PodCIDR: "10.244.0.0/16", ServiceCIDR: "10.96.0.0/12",
		CNI: "cilium", Topology: "single-node", Status: store.ClusterReady, Options: []byte("{}")}
	sid, _ := s.Secrets.Put(ctx, store.SecretKubeconfig, []byte("x"))
	c.KubeconfigSecretID = &sid
	if err := db.CreateCluster(ctx, c); err != nil {
		t.Fatal(err)
	}
	n := &corev1.Node{ObjectMeta: metav1.ObjectMeta{Name: "n1"}, Status: corev1.NodeStatus{Images: []corev1.ContainerImage{
		{Names: []string{"ghcr.io/org/web:1"}, SizeBytes: 1 << 20}}}}
	s.Kube.Inject("c1", sid, fake.NewClientset(n))
	var cat catalogView
	if code := call(t, ts, "GET", "/api/v1/clusters/c1/k8s/images", nil, &cat); code != 200 || len(cat.Images) != 1 || cat.Images[0].Registry != "ghcr" {
		t.Fatalf("catalog: %d %+v", code, cat.Images)
	}
	var job store.Job
	if code := call(t, ts, "POST", "/api/v1/clusters/c1/registries/sync", nil, &job); code != 202 || job.Kind != "cluster.registries-sync" {
		t.Fatalf("sync: %d %+v", code, job)
	}
	if code := call(t, ts, "POST", "/api/v1/clusters/c1/registries/sync", nil, &e); code != 409 {
		t.Fatalf("a second sync while one is pending must conflict: %d", code)
	}
	if code := call(t, ts, "POST", "/api/v1/clusters/c1/images/prepull", map[string]any{"images": []string{"$(reboot)"}}, &e); code != 400 {
		t.Fatalf("invalid image: %d", code)
	}
	if code := call(t, ts, "POST", "/api/v1/clusters/c1/images/prepull", map[string]any{"images": []string{"nginx:1.29"}}, &job); code != 202 {
		t.Fatalf("prepull: %d", code)
	}
	if code := call(t, ts, "POST", "/api/v1/clusters/c1/images/prune", map[string]any{"serverIds": []string{"other"}}, &e); code != 400 {
		t.Fatalf("prune foreign server: %d", code)
	}
	if code := call(t, ts, "DELETE", "/api/v1/registries/"+reg.ID, nil, nil); code != 204 {
		t.Fatalf("delete: %d", code)
	}
	if list, _ := db.ListRegistries(ctx); len(list) != 0 {
		t.Fatal("registry not deleted")
	}
}

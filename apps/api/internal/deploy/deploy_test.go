package deploy

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"k8s.io/apimachinery/pkg/api/meta"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	dynamicfake "k8s.io/client-go/dynamic/fake"
	k8stesting "k8s.io/client-go/testing"
)

func fullApp() AppSpec {
	return AppSpec{Name: "web", Namespace: "shop", Image: "nginx:1.29", Replicas: 2, ContainerPort: 8080, HealthPath: "/healthz",
		Env:        []EnvVar{{Name: "MODE", Value: "prod"}},
		Resources:  Resources{CPURequest: "100m", MemoryRequest: "128Mi", MemoryLimit: "256Mi"},
		Service:    &ServiceSpec{Type: "NodePort", Port: 80, NodePort: 30080},
		Ingress:    &IngressSpec{Host: "shop.example.com", ClassName: "nginx", TLSSecret: "shop-tls"},
		Volume:     &VolumeSpec{Size: "5Gi", MountPath: "/data"},
		PullSecret: &PullSecret{Server: "registry.example.com", Username: "ci", Password: "s3cr3t-pass"}}
}

func TestRenderApp(t *testing.T) {
	a := fullApp()
	if err := a.Normalize(); err != nil {
		t.Fatal(err)
	}
	us, text, err := a.Render()
	if err != nil {
		t.Fatal(err)
	}
	var kinds []string
	for _, u := range us {
		kinds = append(kinds, u.GetKind())
	}
	if strings.Join(kinds, ",") != "Namespace,PersistentVolumeClaim,Deployment,Service,Ingress" {
		t.Fatalf("kinds %v", kinds)
	}
	for _, want := range []string{"image: nginx:1.29", "nodePort: 30080", "claimName: web-data", "type: Recreate", "name: web-registry",
		"ingressClassName: nginx", "secretName: shop-tls", "path: /healthz", "memory: 256Mi", "app.kubernetes.io/managed-by: k8sfront"} {
		if !strings.Contains(text, want) {
			t.Errorf("rendered YAML lacks %q:\n%s", want, text)
		}
	}
	for _, leak := range []string{"s3cr3t", "creationTimestamp", "status:"} {
		if strings.Contains(text, leak) {
			t.Errorf("rendered YAML contains %q", leak)
		}
	}
	sec, err := a.PullSecretObject()
	if err != nil || sec == nil || sec.GetName() != "web-registry" {
		t.Fatalf("pull secret: %v %v", sec, err)
	}
	// The rendered YAML must round-trip through the editor's parser.
	parsed, err := Parse(text)
	if err != nil || len(parsed) != len(us) {
		t.Fatalf("parse rendered: %d %v", len(parsed), err)
	}
}

func TestNormalizeRejects(t *testing.T) {
	for name, mut := range map[string]func(*AppSpec){
		"name":      func(a *AppSpec) { a.Name = "Web_App" },
		"image":     func(a *AppSpec) { a.Image = "nginx; rm -rf /" },
		"quantity":  func(a *AppSpec) { a.Resources.CPURequest = "lots" },
		"nodeport":  func(a *AppSpec) { a.Service.NodePort = 80 },
		"ingress":   func(a *AppSpec) { a.Service = nil },
		"mount":     func(a *AppSpec) { a.Volume.MountPath = "data" },
		"env":       func(a *AppSpec) { a.Env = append(a.Env, EnvVar{Name: "MODE"}) },
		"registry":  func(a *AppSpec) { a.PullSecret.Username = "" },
		"replicas":  func(a *AppSpec) { a.Replicas = 1000 },
		"ingressTL": func(a *AppSpec) { a.Ingress.Host = "" },
	} {
		a := fullApp()
		mut(&a)
		if err := a.Normalize(); !errors.Is(err, ErrInvalid) {
			t.Errorf("%s: expected validation error, got %v", name, err)
		}
	}
}

func TestParseManifests(t *testing.T) {
	objs, err := Parse(`
apiVersion: v1
kind: List
items:
- apiVersion: v1
  kind: ConfigMap
  metadata: {name: a}
---
# comment only
---
apiVersion: apps/v1
kind: Deployment
metadata:
  name: b
`)
	if err != nil || len(objs) != 2 || objs[1].GetKind() != "Deployment" {
		t.Fatalf("%d %v", len(objs), err)
	}
	for _, bad := range []string{"", "kind: ConfigMap\nmetadata: {name: x}", "apiVersion: v1\nkind: ConfigMap\nmetadata: {generateName: x-}", "a: [b"} {
		if _, err := Parse(bad); !errors.Is(err, ErrInvalid) {
			t.Errorf("%q: expected validation error, got %v", bad, err)
		}
	}
}

func TestApplyOrdersAndReports(t *testing.T) {
	scheme := runtime.NewScheme()
	gvrs := map[schema.GroupVersionResource]string{
		{Version: "v1", Resource: "namespaces"}:                 "NamespaceList",
		{Version: "v1", Resource: "configmaps"}:                 "ConfigMapList",
		{Group: "apps", Version: "v1", Resource: "deployments"}: "DeploymentList",
	}
	dyn := dynamicfake.NewSimpleDynamicClientWithCustomListKinds(scheme, gvrs)
	// The fake tracker does not implement server-side apply: echo the object.
	var patched []string
	dyn.PrependReactor("patch", "*", func(action k8stesting.Action) (bool, runtime.Object, error) {
		pa := action.(k8stesting.PatchAction)
		if pa.GetPatchType() != types.ApplyPatchType {
			return false, nil, nil
		}
		u := &unstructured.Unstructured{}
		if err := json.Unmarshal(pa.GetPatch(), &u.Object); err != nil {
			return true, nil, err
		}
		patched = append(patched, u.GetKind()+"@"+pa.GetNamespace())
		u.SetResourceVersion("1")
		return true, u, nil
	})
	mapper := meta.NewDefaultRESTMapper(nil)
	mapper.Add(schema.GroupVersionKind{Version: "v1", Kind: "Namespace"}, meta.RESTScopeRoot)
	mapper.Add(schema.GroupVersionKind{Version: "v1", Kind: "ConfigMap"}, meta.RESTScopeNamespace)
	mapper.Add(schema.GroupVersionKind{Group: "apps", Version: "v1", Kind: "Deployment"}, meta.RESTScopeNamespace)
	objs, err := Parse(`apiVersion: apps/v1
kind: Deployment
metadata: {name: web}
---
apiVersion: v1
kind: ConfigMap
metadata: {name: cfg}
---
apiVersion: example.com/v1
kind: Widget
metadata: {name: w}
---
apiVersion: v1
kind: Namespace
metadata: {name: shop}
`)
	if err != nil {
		t.Fatal(err)
	}
	a := &Applier{Dynamic: dyn, Mapper: resettable{mapper}}
	sum := a.Apply(context.Background(), objs, "shop", false)
	var order []string
	for _, r := range sum.Results {
		order = append(order, r.Kind+":"+r.Action)
	}
	// Namespace first; the unknown kind fails without stopping the others.
	if order[0] != "Namespace:created" || sum.Failed != 1 || len(order) != 4 || len(patched) != 3 {
		t.Fatalf("results %v (%+v)", order, sum.Results)
	}
	for _, r := range sum.Results {
		if r.Kind == "ConfigMap" && r.Namespace != "shop" {
			t.Fatalf("default namespace not applied: %+v", r)
		}
		if r.Kind == "Namespace" && r.Namespace != "" {
			t.Fatalf("cluster-scoped object kept a namespace: %+v", r)
		}
	}
}

type resettable struct{ meta.RESTMapper }

func (resettable) Reset() {}

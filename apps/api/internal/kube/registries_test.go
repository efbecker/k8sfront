package kube

import (
	"context"
	"testing"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes/fake"
)

func nsWithSA(name string) []any {
	return []any{
		&corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: name}},
		&corev1.ServiceAccount{ObjectMeta: metav1.ObjectMeta{Name: "default", Namespace: name},
			ImagePullSecrets: []corev1.LocalObjectReference{{Name: "user-secret"}}},
	}
}

func TestSyncPullSecrets(t *testing.T) {
	ctx := context.Background()
	var objs []any
	for _, ns := range []string{"default", "apps", "kube-system"} {
		objs = append(objs, nsWithSA(ns)...)
	}
	// Left over from a registry that no longer exists.
	objs = append(objs, &corev1.Secret{ObjectMeta: metav1.ObjectMeta{Name: "k8sfront-registry-old", Namespace: "apps",
		Labels: map[string]string{LabelManagedBy: "k8sfront", LabelRegistry: "gone"}}})
	cs := fake.NewClientset()
	for _, o := range objs {
		switch v := o.(type) {
		case *corev1.Namespace:
			cs.CoreV1().Namespaces().Create(ctx, v, metav1.CreateOptions{})
		case *corev1.ServiceAccount:
			if v.Namespace == "apps" {
				v.ImagePullSecrets = append(v.ImagePullSecrets, corev1.LocalObjectReference{Name: "k8sfront-registry-old"})
			}
			cs.CoreV1().ServiceAccounts(v.Namespace).Create(ctx, v, metav1.CreateOptions{})
		case *corev1.Secret:
			cs.CoreV1().Secrets(v.Namespace).Create(ctx, v, metav1.CreateOptions{})
		}
	}
	desired := []PullSecretSpec{
		{RegistryID: "r1", Name: "k8sfront-registry-hub", DockerConfig: []byte(`{"auths":{}}`)},                                // every non-system ns
		{RegistryID: "r2", Name: "k8sfront-registry-ghcr", Namespaces: []string{"apps"}, DockerConfig: []byte(`{"auths":{}}`)}, // only apps
	}
	changes, err := SyncPullSecrets(ctx, cs, nil, desired, "")
	if err != nil {
		t.Fatal(err)
	}
	for _, c := range changes {
		if c.Error != "" {
			t.Fatalf("change error: %+v", c)
		}
	}
	has := func(ns, name string) bool {
		_, err := cs.CoreV1().Secrets(ns).Get(ctx, name, metav1.GetOptions{})
		return err == nil
	}
	if !has("default", "k8sfront-registry-hub") || !has("apps", "k8sfront-registry-hub") || !has("apps", "k8sfront-registry-ghcr") {
		t.Fatal("secrets missing in targeted namespaces")
	}
	if has("default", "k8sfront-registry-ghcr") || has("kube-system", "k8sfront-registry-hub") {
		t.Fatal("secrets created outside their namespaces (or in a system namespace)")
	}
	if has("apps", "k8sfront-registry-old") {
		t.Fatal("stale managed secret must be removed")
	}
	sa, _ := cs.CoreV1().ServiceAccounts("apps").Get(ctx, "default", metav1.GetOptions{})
	var names []string
	for _, r := range sa.ImagePullSecrets {
		names = append(names, r.Name)
	}
	want := []string{"user-secret", "k8sfront-registry-hub", "k8sfront-registry-ghcr"}
	if len(names) != len(want) {
		t.Fatalf("default SA pull secrets = %v, want %v", names, want)
	}
	for i := range want {
		if names[i] != want[i] {
			t.Fatalf("default SA pull secrets = %v, want %v", names, want)
		}
	}
	// Idempotent: a second sync does not duplicate references.
	if _, err := SyncPullSecrets(ctx, cs, nil, desired, ""); err != nil {
		t.Fatal(err)
	}
	sa, _ = cs.CoreV1().ServiceAccounts("apps").Get(ctx, "default", metav1.GetOptions{})
	if len(sa.ImagePullSecrets) != 3 {
		t.Fatalf("duplicated references: %v", sa.ImagePullSecrets)
	}
	// Single-namespace mode never removes anything elsewhere.
	if _, err := SyncPullSecrets(ctx, cs, nil, nil, "default"); err != nil {
		t.Fatal(err)
	}
	if !has("apps", "k8sfront-registry-hub") {
		t.Fatal("namespace-scoped sync must not clean up other namespaces")
	}
}

func TestListImages(t *testing.T) {
	ctx := context.Background()
	n1 := node("n1", true, true)
	n1.Status.Images = []corev1.ContainerImage{
		{Names: []string{"docker.io/library/nginx@sha256:aaa", "docker.io/library/nginx:1.29"}, SizeBytes: 70 << 20},
		{Names: []string{"docker.io/library/redis:7"}, SizeBytes: 40 << 20},
		{Names: []string{"registry.k8s.io/pause:3.10"}, SizeBytes: 1 << 20},
	}
	n2 := node("n2", true, false)
	p := pod("web", "web-7d9f-abc", "n2", corev1.PodRunning, true)
	p.Spec.Containers = []corev1.Container{{Name: "web", Image: "nginx:1.29"}}
	isCtl := true
	p.OwnerReferences = []metav1.OwnerReference{{Kind: "ReplicaSet", Name: "web-7d9f", Controller: &isCtl}}
	sys := pod("kube-system", "kp", "n1", corev1.PodRunning, true)
	sys.Spec.Containers = []corev1.Container{{Name: "p", Image: "registry.k8s.io/pause:3.10"}}
	cs := fake.NewClientset(n1, n2, p, sys)
	cat, err := ListImages(ctx, cs)
	if err != nil {
		t.Fatal(err)
	}
	by := map[string]CatalogImage{}
	for _, im := range cat.Images {
		by[im.Ref] = im
	}
	ng := by["docker.io/library/nginx:1.29"]
	if !ng.InUse || ng.Pods != 1 || len(ng.Nodes) != 2 || ng.SizeBytes != 70<<20 || len(ng.Uses) != 1 || ng.Uses[0].Kind != "Deployment" || ng.Uses[0].Name != "web" {
		t.Fatalf("nginx: %+v", ng)
	}
	if r := by["docker.io/library/redis:7"]; r.InUse || r.System || len(r.Nodes) != 1 {
		t.Fatalf("unused redis: %+v", r)
	}
	if pz := by["registry.k8s.io/pause:3.10"]; !pz.System {
		t.Fatalf("pause is a system image: %+v", pz)
	}
	if len(cat.Images) != 3 {
		t.Fatalf("digest aliases must not duplicate images: %+v", cat.Images)
	}
}

func TestPullStates(t *testing.T) {
	images := []string{"a:1", "b:1", "c:1"}
	p := corev1.Pod{Spec: corev1.PodSpec{NodeName: "n1"}, Status: corev1.PodStatus{ContainerStatuses: []corev1.ContainerStatus{
		{Name: ContainerName(0), State: corev1.ContainerState{Waiting: &corev1.ContainerStateWaiting{Reason: "CrashLoopBackOff"}}},
		{Name: ContainerName(1), State: corev1.ContainerState{Waiting: &corev1.ContainerStateWaiting{Reason: "ImagePullBackOff", Message: "unauthorized"}}},
		{Name: ContainerName(2), State: corev1.ContainerState{Waiting: &corev1.ContainerStateWaiting{Reason: "ContainerCreating"}}},
	}}}
	st := PullStates([]corev1.Pod{p, {}}, images)
	if len(st) != 3 || st[0].State != PullDone || st[1].State != PullFailed || st[2].State != PullPending {
		t.Fatalf("states: %+v", st)
	}
	ds := PrepullDaemonSet("prepull-x", images, []string{"k8sfront-registry-hub"})
	spec := ds.Spec.Template.Spec
	if len(spec.Containers) != 3 || spec.Containers[0].Command[0] != PrepullCommand || len(spec.ImagePullSecrets) != 1 ||
		len(spec.Tolerations) != 1 || spec.Tolerations[0].Operator != corev1.TolerationOpExists {
		t.Fatalf("daemonset: %+v", spec)
	}
}

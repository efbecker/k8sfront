package kube

import (
	"context"
	"io"
	"log/slog"
	"testing"
	"time"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/kubernetes/fake"
)

func node(name string, ready bool, cp bool) *corev1.Node {
	st := corev1.ConditionFalse
	if ready {
		st = corev1.ConditionTrue
	}
	labels := map[string]string{"kubernetes.io/hostname": name}
	if cp {
		labels["node-role.kubernetes.io/control-plane"] = ""
	}
	return &corev1.Node{
		ObjectMeta: metav1.ObjectMeta{Name: name, UID: types.UID("uid-" + name), Labels: labels},
		Status: corev1.NodeStatus{
			Conditions: []corev1.NodeCondition{{Type: corev1.NodeReady, Status: st}},
			Addresses:  []corev1.NodeAddress{{Type: corev1.NodeInternalIP, Address: "10.0.0." + name[len(name)-1:]}},
			Allocatable: corev1.ResourceList{corev1.ResourceCPU: resource.MustParse("2"), corev1.ResourceMemory: resource.MustParse("4Gi"),
				corev1.ResourcePods: resource.MustParse("110")},
			NodeInfo: corev1.NodeSystemInfo{KubeletVersion: "v1.37.2", OSImage: "Debian GNU/Linux 13 (trixie)"},
		},
	}
}

func pod(ns, name, nodeName string, phase corev1.PodPhase, ready bool) *corev1.Pod {
	return &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{Namespace: ns, Name: name, UID: types.UID("uid-" + name),
			Labels:          map[string]string{"app": "web", "pod-template-hash": "abc12"},
			OwnerReferences: []metav1.OwnerReference{{Kind: "ReplicaSet", Name: "web-abc12"}},
		},
		Spec: corev1.PodSpec{NodeName: nodeName, Containers: []corev1.Container{{Name: "c", Image: "nginx:1.29",
			Env: []corev1.EnvVar{{Name: "PASSWORD", Value: "s3cr3t"}},
			Resources: corev1.ResourceRequirements{Requests: corev1.ResourceList{
				corev1.ResourceCPU: resource.MustParse("250m"), corev1.ResourceMemory: resource.MustParse("128Mi")}}}}},
		Status: corev1.PodStatus{Phase: phase, PodIP: "10.244.1.5",
			ContainerStatuses: []corev1.ContainerStatus{{Name: "c", Ready: ready, RestartCount: 2}}},
	}
}

func TestPodViewCollapsesDeploymentAndHidesEnv(t *testing.T) {
	v := podView(pod("default", "web-abc12-xyz", "n1", corev1.PodRunning, true))
	if v.OwnerKind != "Deployment" || v.OwnerName != "web" {
		t.Fatalf("owner = %s/%s", v.OwnerKind, v.OwnerName)
	}
	if v.Ready != 1 || v.Containers != 1 || v.Restarts != 2 {
		t.Fatalf("counts = %+v", v)
	}
	// The slim view has no field that could carry env values.
	for _, img := range v.Images {
		if img != "nginx:1.29" {
			t.Fatalf("image %q", img)
		}
	}
}

func TestOverview(t *testing.T) {
	replicas := int32(2)
	cs := fake.NewClientset(
		node("n1", true, true), node("n2", false, false),
		pod("default", "web-1", "n1", corev1.PodRunning, true),
		pod("default", "web-2", "n2", corev1.PodRunning, false),
		pod("default", "done", "n2", corev1.PodSucceeded, false),
		&corev1.Pod{ObjectMeta: metav1.ObjectMeta{Namespace: "kube-system", Name: "etcd-n1", UID: "etcd",
			Labels: map[string]string{"tier": "control-plane", "component": "etcd"}},
			Spec:   corev1.PodSpec{NodeName: "n1", Containers: []corev1.Container{{Name: "etcd"}}},
			Status: corev1.PodStatus{Phase: corev1.PodRunning, ContainerStatuses: []corev1.ContainerStatus{{Ready: true}}}},
		&corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: "default"}},
		&appsv1.Deployment{ObjectMeta: metav1.ObjectMeta{Namespace: "kube-system", Name: "coredns"},
			Spec: appsv1.DeploymentSpec{Replicas: &replicas}, Status: appsv1.DeploymentStatus{ReadyReplicas: 2}},
		&corev1.Event{ObjectMeta: metav1.ObjectMeta{Namespace: "default", Name: "e1"}, Type: "Warning", Reason: "BackOff",
			Message: "back-off", InvolvedObject: corev1.ObjectReference{Kind: "Pod", Name: "web-2"}, Count: 3,
			LastTimestamp: metav1.NewTime(time.Now())},
	)
	ov, err := GetOverview(context.Background(), cs)
	if err != nil {
		t.Fatal(err)
	}
	if len(ov.Nodes) != 2 || ov.NodesReady != 1 {
		t.Fatalf("nodes = %d ready = %d", len(ov.Nodes), ov.NodesReady)
	}
	if ov.Pods.Total != 4 || ov.Pods.Running != 3 || ov.Pods.Succeeded != 1 || ov.Pods.Problem != 1 {
		t.Fatalf("pods = %+v", ov.Pods)
	}
	if ov.CPU.Allocatable != 4000 || ov.CPU.Requested != 500 {
		t.Fatalf("cpu = %+v", ov.CPU)
	}
	if ov.Nodes[1].Pods != 1 { // the succeeded pod does not count
		t.Fatalf("n2 pods = %d", ov.Nodes[1].Pods)
	}
	if len(ov.ControlPlane) != 1 || ov.ControlPlane[0].Name != "etcd" || !ov.ControlPlane[0].Ready {
		t.Fatalf("control plane = %+v", ov.ControlPlane)
	}
	if ov.Deployments != 1 || ov.DeploymentsReady != 1 || len(ov.System) != 1 {
		t.Fatalf("deployments = %d/%d system=%d", ov.DeploymentsReady, ov.Deployments, len(ov.System))
	}
	if len(ov.Events) != 1 || ov.Events[0].Count != 3 {
		t.Fatalf("events = %+v", ov.Events)
	}
	if ov.MetricsAvailable {
		t.Fatal("fake cluster has no metrics")
	}
}

func recv(t *testing.T, sub *TopoSub) TopoEvent {
	t.Helper()
	select {
	case ev, ok := <-sub.C:
		if !ok {
			t.Fatal("subscription closed")
		}
		return ev
	case <-time.After(5 * time.Second):
		t.Fatal("timeout waiting for topology event")
	}
	return TopoEvent{}
}

func TestTopologyStream(t *testing.T) {
	ctx := context.Background()
	cs := fake.NewClientset(node("n1", true, true), pod("default", "web-1", "n1", corev1.PodRunning, true))
	topo := NewTopology(cs, slog.New(slog.NewTextHandler(io.Discard, nil)))
	defer topo.Stop()

	sub, initial := topo.Subscribe()
	if len(initial) == 0 || initial[0].Type != TopoStatus {
		t.Fatalf("initial = %+v", initial)
	}
	// Not synced yet: status + snapshot arrive on the channel.
	var snap TopoEvent
	for snap.Type != TopoSnapshot {
		snap = recv(t, sub)
	}
	if len(snap.Nodes) != 1 || len(snap.Pods) != 1 {
		t.Fatalf("snapshot = %d nodes, %d pods", len(snap.Nodes), len(snap.Pods))
	}

	p2 := pod("default", "web-2", "n1", corev1.PodPending, false)
	if _, err := cs.CoreV1().Pods("default").Create(ctx, p2, metav1.CreateOptions{}); err != nil {
		t.Fatal(err)
	}
	ev := recv(t, sub)
	if ev.Type != TopoUpsert || ev.Kind != "pod" || ev.Pod.Name != "web-2" || ev.Pod.Phase != "Pending" {
		t.Fatalf("upsert = %+v", ev)
	}
	if err := cs.CoreV1().Pods("default").Delete(ctx, "web-1", metav1.DeleteOptions{}); err != nil {
		t.Fatal(err)
	}
	ev = recv(t, sub)
	if ev.Type != TopoDelete || ev.UID != "uid-web-1" {
		t.Fatalf("delete = %+v", ev)
	}

	// A second subscriber gets the current snapshot synchronously.
	sub2, initial2 := topo.Subscribe()
	if len(initial2) != 2 || initial2[1].Type != TopoSnapshot || len(initial2[1].Pods) != 1 || initial2[1].Pods[0].Name != "web-2" {
		t.Fatalf("initial2 = %+v", initial2)
	}
	topo.Unsubscribe(sub2)
	if _, ok := <-sub2.C; ok {
		t.Fatal("unsubscribed channel should be closed")
	}
	topo.Unsubscribe(sub)
}

func TestTopologyStopsWhenIdle(t *testing.T) {
	old := topoIdleTimeout
	topoIdleTimeout = 10 * time.Millisecond
	defer func() { topoIdleTimeout = old }()

	topo := NewTopology(fake.NewClientset(), slog.New(slog.NewTextHandler(io.Discard, nil)))
	sub, _ := topo.Subscribe()
	topo.Unsubscribe(sub)
	deadline := time.Now().Add(2 * time.Second)
	for {
		topo.mu.Lock()
		running := topo.running
		topo.mu.Unlock()
		if !running {
			return
		}
		if time.Now().After(deadline) {
			t.Fatal("informers still running after idle timeout")
		}
		time.Sleep(5 * time.Millisecond)
	}
}

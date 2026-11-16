package upgrade

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/efbecker/k8sfront/apps/api/internal/k8sver"
	corev1 "k8s.io/api/core/v1"
	policyv1 "k8s.io/api/policy/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/version"
	fakediscovery "k8s.io/client-go/discovery/fake"
	"k8s.io/client-go/kubernetes/fake"
)

func releases(m map[int]string) LatestPatchFunc {
	return func(_ context.Context, major, minor int) (k8sver.Version, error) {
		if s, ok := m[minor]; ok {
			return k8sver.Parse(s)
		}
		return k8sver.Version{}, errors.New("404")
	}
}

func TestTargetsWalkOneMinorAtATime(t *testing.T) {
	cur, _ := k8sver.Parse("v1.35.1")
	got, err := Targets(context.Background(), cur, releases(map[int]string{35: "v1.35.4", 36: "v1.36.9", 37: "v1.37.2"}))
	if err != nil {
		t.Fatal(err)
	}
	want := []struct {
		v, kind, hops string
	}{{"v1.35.4", "patch", "v1.35.4"}, {"v1.36.9", "minor", "v1.36.9"}, {"v1.37.2", "minor", "v1.36.9,v1.37.2"}}
	if len(got) != len(want) {
		t.Fatalf("targets %+v", got)
	}
	for i, w := range want {
		if got[i].Version != w.v || got[i].Kind != w.kind || strings.Join(got[i].Hops, ",") != w.hops {
			t.Errorf("target %d = %+v, want %+v", i, got[i], w)
		}
	}
	if !got[2].Latest || got[0].Latest {
		t.Error("only the newest target is latest")
	}
	if _, err := Resolve(got, "1.37.2"); err != nil {
		t.Fatal(err)
	}
	if _, err := Resolve(got, "v1.38.0"); err == nil {
		t.Fatal("unknown target must be rejected")
	}
	// Already up to date.
	cur, _ = k8sver.Parse("v1.37.2")
	if got, _ := Targets(context.Background(), cur, releases(map[int]string{37: "v1.37.2"})); len(got) != 0 {
		t.Fatalf("expected no targets, got %+v", got)
	}
}

func TestInspect(t *testing.T) {
	ready := corev1.NodeCondition{Type: corev1.NodeReady, Status: corev1.ConditionTrue}
	cs := fake.NewClientset(
		&corev1.Node{ObjectMeta: metav1.ObjectMeta{Name: "cp1", Labels: map[string]string{"node-role.kubernetes.io/control-plane": ""}},
			Status: corev1.NodeStatus{Conditions: []corev1.NodeCondition{ready}, NodeInfo: corev1.NodeSystemInfo{KubeletVersion: "v1.37.2"}}},
		&corev1.Node{ObjectMeta: metav1.ObjectMeta{Name: "w1"}, Spec: corev1.NodeSpec{Unschedulable: true},
			Status: corev1.NodeStatus{Conditions: []corev1.NodeCondition{{Type: corev1.NodeReady, Status: corev1.ConditionFalse}}, NodeInfo: corev1.NodeSystemInfo{KubeletVersion: "v1.36.4"}}},
		&policyv1.PodDisruptionBudget{ObjectMeta: metav1.ObjectMeta{Namespace: "db", Name: "pg"}, Status: policyv1.PodDisruptionBudgetStatus{ExpectedPods: 1, DisruptionsAllowed: 0}},
	)
	cs.Discovery().(*fakediscovery.FakeDiscovery).FakedServerVersion = &version.Info{GitVersion: "v1.37.2"}
	st, err := Inspect(context.Background(), cs)
	if err != nil {
		t.Fatal(err)
	}
	if st.ServerVersion != "v1.37.2" || len(st.Nodes) != 2 || !st.Nodes[0].ControlPlane || !st.Blocked() {
		t.Fatalf("state %+v", st)
	}
	status := map[string]string{}
	for _, c := range st.Checks {
		status[c.ID] = c.Status
	}
	if status["nodes-ready"] != "fail" || status["pdb"] != "warn" || status["skew"] != "warn" || status["cordoned"] != "warn" {
		t.Fatalf("checks %+v", st.Checks)
	}
}

func TestParseCheckExpiration(t *testing.T) {
	out := `[check-expiration] Reading configuration from the "kubeadm-config" ConfigMap in namespace "kube-system"...

CERTIFICATE                EXPIRES                  RESIDUAL TIME   CERTIFICATE AUTHORITY   EXTERNALLY MANAGED
admin.conf                 Nov 16, 2027 13:56 UTC   364d            ca                      no
apiserver-etcd-client      Nov 16, 2027 13:56 UTC   364d            etcd-ca                 no
super-admin.conf           Nov 16, 2027 13:56 UTC   364d            ca                      no

CERTIFICATE AUTHORITY   EXPIRES                  RESIDUAL TIME   EXTERNALLY MANAGED
ca                      Nov 14, 2036 13:56 UTC   9y              no
etcd-ca                 Nov 14, 2036 13:56 UTC   9y              no
`
	certs := ParseCheckExpiration(out)
	if len(certs) != 5 {
		t.Fatalf("parsed %d: %+v", len(certs), certs)
	}
	if certs[0].Name != "admin.conf" || certs[0].Expires.Year() != 2027 || certs[0].Residual != "364d" || certs[0].Authority {
		t.Fatalf("first: %+v", certs[0])
	}
	if !certs[3].Authority || certs[3].Name != "ca" || certs[3].Residual != "9y" {
		t.Fatalf("ca: %+v", certs[3])
	}
}

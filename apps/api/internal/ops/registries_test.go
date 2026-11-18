package ops

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/efbecker/k8sfront/apps/api/internal/bootstrap"
	"github.com/efbecker/k8sfront/apps/api/internal/kube"
	"github.com/efbecker/k8sfront/apps/api/internal/store"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/kubernetes/fake"
)

type fakeKube struct{ cs kubernetes.Interface }

func (f fakeKube) Get(context.Context, string) (*kube.Cluster, error) {
	return &kube.Cluster{ID: "c1", Client: f.cs}, nil
}

func TestRegistriesSyncNodeByNodeAndPullSecrets(t *testing.T) {
	ctx := context.Background()
	f := &lcFake{failOnce: map[string]int{"w1:registries": 1}, hosts: map[string]string{}, boot: map[string]int{}}
	cs := fake.NewClientset(
		&corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: "apps"}},
		&corev1.ServiceAccount{ObjectMeta: metav1.ObjectMeta{Name: "default", Namespace: "apps"}},
	)
	db, engine, sec := newLifecycleEnvKube(t, f, threeNodes, "{}", fakeKube{cs})
	pw, _ := sec.Put(ctx, store.SecretRegistryAuth, []byte("tok3n-value"))
	if err := db.CreateRegistry(ctx, &store.Registry{ID: "r1", Name: "ghcr", Kind: "ghcr", Host: "ghcr.io", Endpoint: "https://ghcr.io",
		Username: "ci", PasswordSecretID: &pw, PullSecret: true}); err != nil {
		t.Fatal(err)
	}
	if err := db.CreateRegistry(ctx, &store.Registry{ID: "r2", Name: "other", Kind: "generic", Host: "r.example.com", Endpoint: "https://r.example.com",
		ClusterIDs: []string{"another-cluster"}}); err != nil {
		t.Fatal(err)
	}
	job, err := engine.Submit(ctx, KindRegistriesSync, "cluster", "c1", ClusterParams{ClusterID: "c1"}, "test")
	if err != nil {
		t.Fatal(err)
	}
	waitJob(t, db, job.ID, store.JobPaused)
	if f.count("w2:registries") != 0 {
		t.Fatal("circuit breaker: w2 must not be touched after w1 failed")
	}
	if f.index("cp1:registries") < 0 || f.index("cp1:registries") > f.index("w1:registries") {
		t.Fatal("control planes go first, one node at a time")
	}
	if err := engine.Retry(ctx, job.ID); err != nil {
		t.Fatal(err)
	}
	done := waitJob(t, db, job.ID, store.JobSucceeded)
	if f.count("w2:registries") != 1 {
		t.Fatal("w2 configured after retry")
	}
	sc, err := cs.CoreV1().Secrets("apps").Get(ctx, "k8sfront-registry-ghcr", metav1.GetOptions{})
	if err != nil || !strings.Contains(string(sc.Data[corev1.DockerConfigJsonKey]), "ghcr.io") {
		t.Fatalf("pull secret: %v", err)
	}
	var res RegistriesSyncResult
	_ = json.Unmarshal(done.Result, &res)
	if len(res.Registries) != 1 || res.Registries[0] != "ghcr" || strings.Contains(string(done.Result), "tok3n") {
		t.Fatalf("result must list only this cluster's registries and no secret: %s", done.Result)
	}
	logs, _ := db.ListLogs(ctx, job.ID, 0, 1000)
	for _, l := range logs {
		if strings.Contains(l.Line, "tok3n") {
			t.Fatal("password leaked to the job log")
		}
	}
}

func TestRegistryHostsRendersOnlyClusterRegistries(t *testing.T) {
	regs := []store.Registry{
		{Name: "hub", Host: "docker.io", Endpoint: "https://registry-1.docker.io", Mirrors: []string{"https://cache:5000"}, CACert: "-----BEGIN CERTIFICATE-----\n"},
		{Name: "x", Host: "x.example.com", Endpoint: "https://x.example.com", ClusterIDs: []string{"other"}},
	}
	hosts, names := RegistryHosts(regs, "c1")
	if len(hosts) != 1 || names[0] != "hub" || !strings.Contains(hosts[0].TOML, `ca = "/etc/containerd/certs.d/docker.io/ca.crt"`) {
		t.Fatalf("hosts: %+v", hosts)
	}
	// The script must be valid even with the CA embedded.
	if !strings.Contains(bootstrap.RegistriesScript(hosts), "ca.crt") {
		t.Fatal("ca not written")
	}
}

func TestImagePrune(t *testing.T) {
	ctx := context.Background()
	f := &lcFake{failOnce: map[string]int{}, hosts: map[string]string{}, boot: map[string]int{}}
	db, engine := newLifecycleEnv(t, f, threeNodes)
	job, err := engine.Submit(ctx, KindImagePrune, "cluster", "c1", PruneParams{ClusterID: "c1", ServerIDs: []string{"w1", "w2"}}, "test")
	if err != nil {
		t.Fatal(err)
	}
	done := waitJob(t, db, job.ID, store.JobSucceeded)
	if f.count("cp1:prune") != 0 || f.count("w1:prune") != 1 || f.count("w2:prune") != 1 {
		t.Fatalf("prune ran on the wrong nodes: %v", f.log)
	}
	var res struct{ Nodes []PruneNode }
	_ = json.Unmarshal(done.Result, &res)
	if len(res.Nodes) != 2 || res.Nodes[0].Removed != 3 || res.Nodes[0].FreedBytes != 1<<20 {
		t.Fatalf("result: %s", done.Result)
	}
}

func TestImagePrepull(t *testing.T) {
	ctx := context.Background()
	f := &lcFake{failOnce: map[string]int{}, hosts: map[string]string{}, boot: map[string]int{}}
	cs := fake.NewClientset()
	db, engine, _ := newLifecycleEnvKube(t, f, threeNodes, "{}", fakeKube{cs})
	// Play the DaemonSet controller + kubelet: one pod per node once the
	// DaemonSet exists, with the image pulled on n1 and failing on n2.
	stop := make(chan struct{})
	defer close(stop)
	go func() {
		for {
			select {
			case <-stop:
				return
			case <-time.After(5 * time.Millisecond):
			}
			list, _ := cs.AppsV1().DaemonSets(kube.PrepullNamespace).List(ctx, metav1.ListOptions{})
			for _, ds := range list.Items {
				if ds.Status.DesiredNumberScheduled == 2 {
					continue
				}
				ds.Status.DesiredNumberScheduled = 2
				cs.AppsV1().DaemonSets(kube.PrepullNamespace).UpdateStatus(ctx, &ds, metav1.UpdateOptions{})
				for i, reason := range []string{"CrashLoopBackOff", "ErrImagePull"} {
					node := []string{"n1", "n2"}[i]
					cs.CoreV1().Pods(kube.PrepullNamespace).Create(ctx, &corev1.Pod{
						ObjectMeta: metav1.ObjectMeta{Name: ds.Name + "-" + node, Labels: ds.Spec.Template.Labels},
						Spec:       corev1.PodSpec{NodeName: node},
						Status: corev1.PodStatus{ContainerStatuses: []corev1.ContainerStatus{{Name: kube.ContainerName(0),
							State: corev1.ContainerState{Waiting: &corev1.ContainerStateWaiting{Reason: reason, Message: "denied"}}}}},
					}, metav1.CreateOptions{})
				}
			}
		}
	}()
	job, err := engine.Submit(ctx, KindImagePrepull, "cluster", "c1", PrepullParams{ClusterID: "c1", Images: []string{"ghcr.io/org/app:1"}}, "test")
	if err != nil {
		t.Fatal(err)
	}
	done := waitJob(t, db, job.ID, store.JobFailed)
	var res PrepullResult
	_ = json.Unmarshal(done.Result, &res)
	if len(res.States) != 2 || res.States[0].State != kube.PullDone || res.States[1].State != kube.PullFailed {
		t.Fatalf("states: %+v", res.States)
	}
	if list, _ := cs.AppsV1().DaemonSets(kube.PrepullNamespace).List(ctx, metav1.ListOptions{}); len(list.Items) != 0 {
		t.Fatal("the temporary DaemonSet must be deleted")
	}
}

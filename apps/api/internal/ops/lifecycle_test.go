package ops

import (
	"context"
	"crypto/rand"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/efbecker/k8sfront/apps/api/internal/bootstrap"
	"github.com/efbecker/k8sfront/apps/api/internal/jobs"
	"github.com/efbecker/k8sfront/apps/api/internal/redact"
	"github.com/efbecker/k8sfront/apps/api/internal/remote"
	"github.com/efbecker/k8sfront/apps/api/internal/secrets"
	"github.com/efbecker/k8sfront/apps/api/internal/sshx"
	"github.com/efbecker/k8sfront/apps/api/internal/store"
	"github.com/efbecker/k8sfront/apps/api/internal/store/sqlite"
	"github.com/efbecker/k8sfront/apps/api/internal/vault"
)

// lcFake simulates a provisioned cluster for lifecycle jobs. Entries in log
// are "runner:op" or "runner:op>node" for kubectl actions about a node.
type lcFake struct {
	mu       sync.Mutex
	log      []string
	failOnce map[string]int    // key -> failures left
	hosts    map[string]string // server name -> IP
	boot     map[string]int
	ca       string
}

type lcRunner struct {
	f    *lcFake
	node string
}

var targetNode = regexp.MustCompile(`(?:node=|kubectl uncordon |kubectl delete node )'([^']+)'`)

func lcClassify(script string) string {
	markers := []struct{ marker, op string }{
		{"devolvido ao serviço", "restore"}, {"K8SFRONT_REGISTRIES_SYNC", "registries"}, {"K8SFRONT_IMAGE_PRUNE", "prune"},
		{"K8SFRONT_APIPROXY_SYNC", "apiproxy"}, {"member remove", "etcd-member"},
		{"APIs depreciadas", "precheck"}, {"snapshot save", "etcd-snapshot"}, {"pkgs.k8s.io/core:/stable", "repo"},
		{"kubeadm upgrade apply", "kubeadm-apply"}, {"kubeadm upgrade node", "kubeadm-node"}, {"apt-mark unhold kubelet kubectl", "kubelet"},
		{"kubectl cordon", "drain"}, {"kubeletVersion}'", "wait"}, {"kubectl uncordon", "uncordon"}, {"get nodes -o json", "get-nodes"},
		{"--with-new-pkgs", "os-upgrade"}, {"reboot-required ]; then echo yes", "reboot-required"}, {bootstrap.ReadBootID, "boot-id"},
		{"systemctl reboot", "reboot"}, {"is-active --quiet kubelet", "wait-kubelet"}, {"kubeadm token create", "join-credentials"},
		{"kubeadm join --config", "join"}, {"apt-mark hold kubelet kubeadm kubectl", "packages"}, {bootstrap.ReadCACert, "read-ca"},
		{"kubectl delete node", "delete-node"}, {"kubeadm reset", "reset"},
	}
	for _, m := range markers {
		if strings.Contains(script, m.marker) {
			op := m.op
			if mm := targetNode.FindStringSubmatch(script); mm != nil && (op == "drain" || op == "restore" || op == "wait" || op == "uncordon" || op == "etcd-snapshot" || op == "delete-node") {
				op += ">" + mm[1]
			}
			return op
		}
	}
	return "other"
}

func (r *lcRunner) Run(ctx context.Context, script string, opts sshx.RunOptions, sink sshx.Sink) (*sshx.Result, error) {
	op := lcClassify(script)
	f := r.f
	f.mu.Lock()
	defer f.mu.Unlock()
	key := r.node + ":" + op
	f.log = append(f.log, key)
	if f.failOnce[key] > 0 {
		f.failOnce[key]--
		if sink != nil {
			sink(store.StreamStdout, "Reading package lists...")
			sink(store.StreamStderr, "E: Failed to fetch https://pkgs.k8s.io/x.deb  Connection timed out")
		}
		return &sshx.Result{ExitCode: 100}, &sshx.ExitError{Code: 100}
	}
	switch op {
	case "get-nodes":
		var items []string
		for name, ip := range f.hosts {
			items = append(items, fmt.Sprintf(`{"metadata":{"name":"k8s-%s","labels":{}},"status":{"conditions":[{"type":"Ready","status":"True"}],"addresses":[{"type":"InternalIP","address":%q}]}}`, name, ip))
		}
		return &sshx.Result{Stdout: `{"items":[` + strings.Join(items, ",") + `]}`}, nil
	case "boot-id":
		return &sshx.Result{Stdout: fmt.Sprintf("boot-%d\n", f.boot[r.node])}, nil
	case "reboot":
		f.boot[r.node]++
	case "reboot-required":
		return &sshx.Result{Stdout: "yes\n"}, nil
	case "read-ca":
		return &sshx.Result{Stdout: f.ca}, nil
	case "prune":
		return &sshx.Result{Stdout: "K8SFRONT_PRUNE removed=3 remaining=7 freed=1048576\n"}, nil
	}
	return &sshx.Result{}, nil
}

func (r *lcRunner) Redactor() *redact.Redactor { return redact.New() }
func (r *lcRunner) Close() error               { return nil }

func (f *lcFake) index(entry string) int {
	f.mu.Lock()
	defer f.mu.Unlock()
	for i, e := range f.log {
		if e == entry {
			return i
		}
	}
	return -1
}

func (f *lcFake) count(entry string) int {
	f.mu.Lock()
	defer f.mu.Unlock()
	n := 0
	for _, e := range f.log {
		if e == entry {
			n++
		}
	}
	return n
}

func newLifecycleEnv(t *testing.T, f *lcFake, nodes []struct {
	name string
	role store.NodeRole
}) (*sqlite.DB, *jobs.Engine) {
	t.Helper()
	return newLifecycleEnvWith(t, f, nodes, "{}")
}

func newLifecycleEnvWith(t *testing.T, f *lcFake, nodes []struct {
	name string
	role store.NodeRole
}, options string) (*sqlite.DB, *jobs.Engine) {
	t.Helper()
	db, engine, _ := newLifecycleEnvKube(t, f, nodes, options, nil)
	return db, engine
}

func newLifecycleEnvKube(t *testing.T, f *lcFake, nodes []struct {
	name string
	role store.NodeRole
}, options string, kubeClients KubeClients) (*sqlite.DB, *jobs.Engine, *secrets.Manager) {
	t.Helper()
	ctx := context.Background()
	db, err := sqlite.Open(ctx, filepath.Join(t.TempDir(), "db.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	key := make([]byte, 32)
	rand.Read(key)
	v, _ := vault.New(key)
	sec := secrets.NewManager(db, v)
	kc, _ := sec.Put(ctx, store.SecretKubeconfig, []byte("apiVersion: v1\nkind: Config\n"))
	cl := &store.Cluster{ID: "c1", Name: "prod", KubernetesVersion: "v1.37.2", PodCIDR: "10.244.0.0/16", ServiceCIDR: "10.96.0.0/12",
		CNI: "cilium", Topology: bootstrap.TopologyMulti, Options: []byte(options), Status: store.ClusterReady, KubeconfigSecretID: &kc}
	if len(nodes) == 1 {
		cl.Topology = bootstrap.TopologySingle
	}
	if err := db.CreateCluster(ctx, cl); err != nil {
		t.Fatal(err)
	}
	for i, n := range nodes {
		cid := "c1"
		ip := fmt.Sprintf("192.168.10.%d", 11+i)
		f.hosts[n.name] = ip
		if err := db.CreateServer(ctx, &store.Server{ID: n.name, ClusterID: &cid, Name: n.name, Host: ip, Port: 22, SSHUser: "root",
			AuthMethod: store.AuthPassword, Role: n.role}); err != nil {
			t.Fatal(err)
		}
	}
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	engine := jobs.New(db, jobs.NewHub(db, logger), logger, 2)
	o := &Ops{Store: db, Secrets: sec, Kube: kubeClients, PollInterval: 5 * time.Millisecond, RebootTimeout: 5 * time.Second, RetryDelay: -1,
		Open: func(_ context.Context, s *store.Server, _ remote.Hooks) (Runner, error) {
			return &lcRunner{f: f, node: s.Name}, nil
		}}
	o.Register(engine)
	if err := engine.Start(ctx); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		sctx, cancel := context.WithTimeout(ctx, 5*time.Second)
		defer cancel()
		engine.Shutdown(sctx)
	})
	return db, engine, sec
}

var threeNodes = []struct {
	name string
	role store.NodeRole
}{{"cp1", store.RoleControlPlane}, {"w1", store.RoleWorker}, {"w2", store.RoleWorker}}

func TestUpgradeRetriesTransientFailureWithoutPausing(t *testing.T) {
	ctx := context.Background()
	f := &lcFake{failOnce: map[string]int{"w1:kubelet": 1}, hosts: map[string]string{}, boot: map[string]int{}}
	db, engine := newLifecycleEnv(t, f, threeNodes)
	job, err := engine.Submit(ctx, KindUpgrade, "cluster", "c1", UpgradeParams{ClusterID: "c1", From: "v1.37.2", Hops: []string{"v1.38.1"}}, "test")
	if err != nil {
		t.Fatal(err)
	}
	waitJob(t, db, job.ID, store.JobSucceeded)
	for entry, want := range map[string]int{"w1:kubelet": 2, "cp1:drain>k8s-w1": 2, "cp1:restore>k8s-w1": 0, "cp1:uncordon>k8s-w1": 1, "w1:kubeadm-node": 1} {
		if got := f.count(entry); got != want {
			t.Errorf("%s ran %d times, want %d (log %v)", entry, got, want, f.log)
		}
	}
	// The retry happens on w1 before w2 is touched.
	if f.index("w2:repo") < f.index("cp1:uncordon>k8s-w1") {
		t.Fatalf("w2 must start only after w1 succeeded: %v", f.log)
	}
}

func TestUpgradeOneNodeAtATimeWithCircuitBreaker(t *testing.T) {
	ctx := context.Background()
	// Fails every automatic attempt (3): only then the job pauses.
	f := &lcFake{failOnce: map[string]int{"w1:kubelet": defaultRetryAttempts}, hosts: map[string]string{}, boot: map[string]int{}}
	db, engine := newLifecycleEnv(t, f, threeNodes)

	if _, err := ValidateHops("v1.36.4", []string{"v1.38.1"}); err == nil {
		t.Fatal("skipping a minor must be rejected")
	}
	job, err := engine.Submit(ctx, KindUpgrade, "cluster", "c1", UpgradeParams{ClusterID: "c1", From: "v1.37.2", Hops: []string{"v1.38.1"}}, "test")
	if err != nil {
		t.Fatal(err)
	}
	paused := waitJob(t, db, job.ID, store.JobPaused)
	if !strings.Contains(paused.Error, "w1") || !strings.Contains(paused.Error, "status 100: E: Failed to fetch") || !strings.Contains(paused.Error, "3 tentativas") {
		t.Fatalf("pause error should name the node, apt's E: line and the attempts: %q", paused.Error)
	}
	// Circuit breaker: w2 was not touched; w1 is not upgraded but goes back
	// to service if still Ready (restore, only after the last attempt), never
	// through the normal uncordon.
	if f.count("w2:repo") != 0 || f.count("w2:kubeadm-node") != 0 || f.count("cp1:uncordon>k8s-w1") != 0 {
		t.Fatalf("job continued past the failing node: %v", f.log)
	}
	if f.count("w1:kubelet") != 3 || f.count("cp1:restore>k8s-w1") != 1 {
		t.Fatalf("want 3 attempts and one restore: %v", f.log)
	}
	if f.index("cp1:restore>k8s-w1") < f.index("w1:kubelet") {
		t.Fatalf("failed node must be restored after the failure: %v", f.log)
	}
	// Primary: snapshot → kubeadm apply → drain → kubelet → Ready → uncordon, then w1.
	seq := []string{"cp1:precheck", "cp1:etcd-snapshot>k8s-cp1", "cp1:repo", "cp1:kubeadm-apply", "cp1:drain>k8s-cp1", "cp1:kubelet",
		"cp1:wait>k8s-cp1", "cp1:uncordon>k8s-cp1", "w1:repo", "w1:kubeadm-node", "cp1:drain>k8s-w1", "w1:kubelet"}
	for i := 1; i < len(seq); i++ {
		if a, b := f.index(seq[i-1]), f.index(seq[i]); a < 0 || b < 0 || a > b {
			t.Fatalf("%s must run before %s: %v", seq[i-1], seq[i], f.log)
		}
	}

	if err := engine.Retry(ctx, job.ID); err != nil {
		t.Fatal(err)
	}
	waitJob(t, db, job.ID, store.JobSucceeded)
	for entry, want := range map[string]int{"cp1:kubeadm-apply": 1, "cp1:precheck": 1, "w1:kubeadm-node": 1, "w1:kubelet": 4,
		"cp1:drain>k8s-w1": 4, "cp1:drain>k8s-cp1": 1, "cp1:restore>k8s-w1": 1,
		"cp1:uncordon>k8s-w1": 1, "w2:kubeadm-node": 1, "cp1:uncordon>k8s-w2": 1, "cp1:etcd-snapshot>k8s-cp1": 1} {
		if got := f.count(entry); got != want {
			t.Errorf("%s ran %d times, want %d (log %v)", entry, got, want, f.log)
		}
	}
	if f.index("cp1:uncordon>k8s-w1") > f.index("w2:repo") {
		t.Fatal("w2 must start only after w1 is uncordoned")
	}
	if cl, _ := db.GetCluster(ctx, "c1"); cl.KubernetesVersion != "v1.38.1" {
		t.Fatalf("cluster version = %s", cl.KubernetesVersion)
	}
	var res UpgradeResult
	j, _ := db.GetJob(ctx, job.ID)
	if err := json.Unmarshal(j.Result, &res); err != nil || res.To != "v1.38.1" {
		t.Fatalf("result %s %v", j.Result, err)
	}
}

func TestRollingRebootWaitsForNewBoot(t *testing.T) {
	ctx := context.Background()
	f := &lcFake{failOnce: map[string]int{}, hosts: map[string]string{}, boot: map[string]int{}}
	db, engine := newLifecycleEnv(t, f, threeNodes)
	job, err := engine.Submit(ctx, KindOSUpdate, "cluster", "c1", OSUpdateParams{ClusterID: "c1", ServerIDs: []string{"w1", "w2"}, Reboot: RebootIfRequired}, "test")
	if err != nil {
		t.Fatal(err)
	}
	waitJob(t, db, job.ID, store.JobSucceeded)
	if f.count("cp1:os-upgrade") != 0 || f.boot["w1"] != 1 || f.boot["w2"] != 1 {
		t.Fatalf("only the selected nodes are patched and rebooted: %v %v", f.boot, f.log)
	}
	seq := []string{"w1:os-upgrade", "cp1:drain>k8s-w1", "w1:reboot", "w1:wait-kubelet", "cp1:wait>k8s-w1", "cp1:uncordon>k8s-w1", "w2:os-upgrade", "w2:reboot"}
	for i := 1; i < len(seq); i++ {
		if a, b := f.index(seq[i-1]), f.index(seq[i]); a < 0 || b < 0 || a > b {
			t.Fatalf("%s must run before %s: %v", seq[i-1], seq[i], f.log)
		}
	}
}

func TestNodeAddAndRemove(t *testing.T) {
	ctx := context.Background()
	f := &lcFake{failOnce: map[string]int{}, hosts: map[string]string{}, boot: map[string]int{}, ca: testCA(t)}
	db, engine := newLifecycleEnv(t, f, threeNodes)
	job, err := engine.Submit(ctx, KindNodeAdd, "cluster", "c1", NodeParams{ClusterID: "c1", ServerID: "w2"}, "test")
	if err != nil {
		t.Fatal(err)
	}
	waitJob(t, db, job.ID, store.JobSucceeded)
	if !(f.index("w2:packages") < f.index("cp1:join-credentials") && f.index("cp1:join-credentials") < f.index("w2:join") && f.index("w2:join") < f.index("cp1:wait>k8s-w2")) {
		t.Fatalf("unexpected node-add order: %v", f.log)
	}
	if f.count("w1:join") != 0 || f.count("w1:packages") != 0 {
		t.Fatal("node-add must only touch the new node")
	}

	job, err = engine.Submit(ctx, KindNodeRemove, "cluster", "c1", NodeParams{ClusterID: "c1", ServerID: "w1"}, "test")
	if err != nil {
		t.Fatal(err)
	}
	waitJob(t, db, job.ID, store.JobSucceeded)
	if !(f.index("cp1:drain>k8s-w1") < f.index("cp1:delete-node>k8s-w1") && f.index("cp1:delete-node>k8s-w1") < f.index("w1:reset")) {
		t.Fatalf("unexpected node-remove order: %v", f.log)
	}
	if s, _ := db.GetServer(ctx, "w1"); s.ClusterID != nil {
		t.Fatal("removed server must be detached from the cluster")
	}
	job, _ = engine.Submit(ctx, KindNodeRemove, "cluster", "c1", NodeParams{ClusterID: "c1", ServerID: "cp1"}, "test")
	waitJob(t, db, job.ID, store.JobFailed)

	// "Remover servidor": same graceful flow, then gone from the inventory.
	job, _ = engine.Submit(ctx, KindNodeRemove, "cluster", "c1", NodeParams{ClusterID: "c1", ServerID: "w2", DeleteServer: true}, "test")
	waitJob(t, db, job.ID, store.JobSucceeded)
	if f.index("cp1:drain>k8s-w2") < 0 || f.index("w2:reset") < f.index("cp1:delete-node>k8s-w2") {
		t.Fatalf("unexpected order: %v", f.log)
	}
	if _, err := db.GetServer(ctx, "w2"); err == nil {
		t.Fatal("server must be deleted from the inventory")
	}
}

func TestControlPlaneAddAndRemoveWithLocalProxy(t *testing.T) {
	ctx := context.Background()
	f := &lcFake{failOnce: map[string]int{}, hosts: map[string]string{}, boot: map[string]int{}, ca: testCA(t)}
	// cp0 is the control plane being added (already attached by the API); it
	// sorts first, so the job must not use it as the primary.
	nodes := []struct {
		name string
		role store.NodeRole
	}{{"cp0", store.RoleControlPlane}, {"cp1", store.RoleControlPlane}, {"w1", store.RoleWorker}}
	db, engine := newLifecycleEnvWith(t, f, nodes, `{"endpointMode":"local-proxy"}`)
	job, err := engine.Submit(ctx, KindNodeAdd, "cluster", "c1", NodeParams{ClusterID: "c1", ServerID: "cp0"}, "test")
	if err != nil {
		t.Fatal(err)
	}
	waitJob(t, db, job.ID, store.JobSucceeded)
	seq := []string{"cp0:apiproxy", "cp0:packages", "cp1:join-credentials", "cp0:join", "cp1:wait>k8s-cp0"}
	for i := 1; i < len(seq); i++ {
		if a, b := f.index(seq[i-1]), f.index(seq[i]); a < 0 || b < 0 || a > b {
			t.Fatalf("%s must run before %s: %v", seq[i-1], seq[i], f.log)
		}
	}
	for _, n := range []string{"cp1", "w1"} {
		if i := f.index(n + ":apiproxy"); i < 0 || i > f.index("cp0:join") {
			t.Fatalf("%s must learn the new control plane before it joins: %v", n, f.log)
		}
	}
	if f.count("cp0:join-credentials") != 0 || f.count("cp1:join") != 0 {
		t.Fatalf("the new control plane must not act as primary: %v", f.log)
	}

	job, err = engine.Submit(ctx, KindNodeRemove, "cluster", "c1", NodeParams{ClusterID: "c1", ServerID: "cp0"}, "test")
	if err != nil {
		t.Fatal(err)
	}
	waitJob(t, db, job.ID, store.JobSucceeded)
	seq = []string{"cp1:drain>k8s-cp0", "cp0:reset", "cp1:etcd-member", "cp1:delete-node>k8s-cp0"}
	for i := 1; i < len(seq); i++ {
		if a, b := f.index(seq[i-1]), f.index(seq[i]); a < 0 || b < 0 || a > b {
			t.Fatalf("%s must run before %s: %v", seq[i-1], seq[i], f.log)
		}
	}
	if f.count("cp1:apiproxy") != 2 || f.count("w1:apiproxy") != 2 || f.count("cp0:apiproxy") != 1 {
		t.Fatalf("every remaining node drops the control plane from its proxy: %v", f.log)
	}
	if s, _ := db.GetServer(ctx, "cp0"); s.ClusterID != nil {
		t.Fatal("removed control plane must be detached")
	}
	if cl, _ := db.GetCluster(ctx, "c1"); cl.APIServerURL != "https://"+f.hosts["cp1"]+":6443" {
		t.Fatalf("API URL must point at a remaining control plane: %s", cl.APIServerURL)
	}
	// The last control plane stays.
	job, _ = engine.Submit(ctx, KindNodeRemove, "cluster", "c1", NodeParams{ClusterID: "c1", ServerID: "cp1"}, "test")
	waitJob(t, db, job.ID, store.JobFailed)
}

func TestControlPlaneAddRefusedOnDirectEndpoint(t *testing.T) {
	ctx := context.Background()
	f := &lcFake{failOnce: map[string]int{}, hosts: map[string]string{}, boot: map[string]int{}, ca: testCA(t)}
	nodes := []struct {
		name string
		role store.NodeRole
	}{{"cp1", store.RoleControlPlane}, {"cp2", store.RoleControlPlane}}
	// Legacy options (no endpoint mode, no VIP): the spec itself is invalid.
	db, engine := newLifecycleEnvWith(t, f, nodes, "{}")
	job, _ := engine.Submit(ctx, KindNodeAdd, "cluster", "c1", NodeParams{ClusterID: "c1", ServerID: "cp2"}, "test")
	waitJob(t, db, job.ID, store.JobFailed)
	if f.count("cp2:join") != 0 {
		t.Fatal("must not join")
	}
}

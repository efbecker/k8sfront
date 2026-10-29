package ops

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/json"
	"encoding/pem"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"math/big"
	"path/filepath"
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

// fakeCluster plays the role of the remote hosts: it classifies each script
// and records the order in which they ran.
type fakeCluster struct {
	mu        sync.Mutex
	log       []string // "node:op"
	failOnce  map[string]bool
	ca        string
	names     map[string]string // server id -> name
	joined    int
	redactors []*redact.Redactor
}

type fakeRunner struct {
	fc   *fakeCluster
	node string
	red  *redact.Redactor
}

func classify(script string) string {
	markers := []struct{ marker, op string }{
		{"apt-mark hold kubelet", "packages"}, {"Desativando swap", "swap"}, {"modules-load.d/k8s.conf", "kernel"}, {"SystemdCgroup", "containerd-or-packages"},
		{"pkgs.k8s.io/core:/stable", "repo"}, {"config images pull", "images"}, {"kubeadm init --config", "init"},
		{"super-admin.conf#", "kube-vip-finalize"}, {"kubeadm token create", "join-credentials"}, {"kubeadm join --config", "join"},
		{"tigera-operator", "cni"}, {"cilium install", "cni"}, {"metrics-server", "metrics-server"}, {"node-role.kubernetes.io/control-plane:NoSchedule-", "untaint"},
		{bootstrap.ReadCACert, "read-ca"}, {bootstrap.ReadAdminConf, "read-admin"}, {"get nodes -o json", "get-nodes"},
	}
	for _, m := range markers {
		if strings.Contains(script, m.marker) {
			if m.op == "containerd-or-packages" {
				if strings.Contains(script, "apt-mark hold") {
					return "packages"
				}
				return "containerd"
			}
			return m.op
		}
	}
	return "unknown"
}

func (f *fakeRunner) Run(ctx context.Context, script string, opts sshx.RunOptions, sink sshx.Sink) (*sshx.Result, error) {
	op := classify(script)
	f.fc.mu.Lock()
	defer f.fc.mu.Unlock()
	f.fc.log = append(f.fc.log, f.node+":"+op)
	if sink != nil && !opts.Quiet {
		sink(store.StreamStdout, f.red.String("running "+op))
	}
	key := f.node + ":" + op
	if f.fc.failOnce[key] {
		delete(f.fc.failOnce, key)
		return &sshx.Result{ExitCode: 1}, &sshx.ExitError{Code: 1}
	}
	switch op {
	case "read-ca":
		return &sshx.Result{Stdout: f.fc.ca}, nil
	case "read-admin":
		return &sshx.Result{Stdout: "apiVersion: v1\nkind: Config\nusers:\n- name: kubernetes-admin\n  user:\n    client-key-data: c2VjcmV0\n"}, nil
	case "join":
		f.fc.joined++
	case "get-nodes":
		var items []string
		for _, name := range f.fc.names {
			items = append(items, fmt.Sprintf(`{"metadata":{"name":%q,"labels":{}},"status":{"conditions":[{"type":"Ready","status":"True"}],"nodeInfo":{"kubeletVersion":"v1.37.2","osImage":"Debian GNU/Linux 13 (trixie)"}}}`, name))
		}
		return &sshx.Result{Stdout: `{"items":[` + strings.Join(items, ",") + `]}`}, nil
	}
	return &sshx.Result{}, nil
}

func (f *fakeRunner) Redactor() *redact.Redactor { return f.red }
func (f *fakeRunner) Close() error               { return nil }

func (fc *fakeCluster) count(entry string) int {
	fc.mu.Lock()
	defer fc.mu.Unlock()
	n := 0
	for _, e := range fc.log {
		if e == entry {
			n++
		}
	}
	return n
}

func (fc *fakeCluster) index(entry string) int {
	fc.mu.Lock()
	defer fc.mu.Unlock()
	for i, e := range fc.log {
		if e == entry {
			return i
		}
	}
	return -1
}

func testCA(t *testing.T) string {
	key, _ := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	tpl := &x509.Certificate{SerialNumber: big.NewInt(1), Subject: pkix.Name{CommonName: "kubernetes"}, NotBefore: time.Now(),
		NotAfter: time.Now().Add(time.Hour), IsCA: true, BasicConstraintsValid: true}
	der, _ := x509.CreateCertificate(rand.Reader, tpl, tpl, &key.PublicKey, key)
	return string(pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}))
}

func waitJob(t *testing.T, st store.Store, id string, want store.JobStatus) *store.Job {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		if j, err := st.GetJob(context.Background(), id); err == nil && j.Status == want {
			return j
		}
		time.Sleep(20 * time.Millisecond)
	}
	j, _ := st.GetJob(context.Background(), id)
	t.Fatalf("job status %s (%s), want %s", j.Status, j.Error, want)
	return nil
}

func TestBootstrapHAOrderingAndResume(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	db, err := sqlite.Open(ctx, filepath.Join(dir, "db.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	key := make([]byte, 32)
	rand.Read(key)
	v, _ := vault.New(key)
	sec := secrets.NewManager(db, v)

	opts, _ := json.Marshal(bootstrap.Options{MetricsServer: true, KubeVIP: &bootstrap.KubeVIP{Address: "192.168.10.50"}})
	cl := &store.Cluster{ID: "c1", Name: "prod", KubernetesVersion: "v1.37.2", PodCIDR: "10.244.0.0/16", ServiceCIDR: "10.96.0.0/12",
		CNI: "calico", Topology: bootstrap.TopologyMulti, Options: opts}
	if err := db.CreateCluster(ctx, cl); err != nil {
		t.Fatal(err)
	}
	fc := &fakeCluster{failOnce: map[string]bool{"w2:join": true}, ca: testCA(t), names: map[string]string{}}
	for i, n := range []struct {
		name string
		role store.NodeRole
	}{{"cp1", store.RoleControlPlane}, {"cp2", store.RoleControlPlane}, {"cp3", store.RoleControlPlane}, {"w1", store.RoleWorker}, {"w2", store.RoleWorker}} {
		id := n.name
		cid := "c1"
		if err := db.CreateServer(ctx, &store.Server{ID: id, ClusterID: &cid, Name: n.name, Host: fmt.Sprintf("192.168.10.%d", 11+i),
			Port: 22, SSHUser: "root", AuthMethod: store.AuthPassword, Role: n.role}); err != nil {
			t.Fatal(err)
		}
		fc.names[id] = n.name
	}

	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	engine := jobs.New(db, jobs.NewHub(db, logger), logger, 2)
	o := &Ops{Store: db, Secrets: sec, PollInterval: 10 * time.Millisecond, ReadyTimeout: 5 * time.Second,
		Open: func(_ context.Context, s *store.Server, _ remote.Hooks) (Runner, error) {
			r := &fakeRunner{fc: fc, node: s.Name, red: redact.New()}
			fc.mu.Lock()
			fc.redactors = append(fc.redactors, r.red)
			fc.mu.Unlock()
			return r, nil
		}}
	o.Register(engine)
	if err := engine.Start(ctx); err != nil {
		t.Fatal(err)
	}
	defer func() {
		sctx, cancel := context.WithTimeout(ctx, 5*time.Second)
		defer cancel()
		engine.Shutdown(sctx)
	}()

	job, err := engine.Submit(ctx, KindBootstrap, "cluster", "c1", BootstrapParams{ClusterID: "c1"}, "test")
	if err != nil {
		t.Fatal(err)
	}
	failed := waitJob(t, db, job.ID, store.JobFailed)
	if got, _ := db.GetCluster(ctx, "c1"); got.Status != store.ClusterFailed {
		t.Fatalf("cluster status after failure = %s", got.Status)
	}
	if !strings.Contains(failed.Error, "w2") {
		t.Fatalf("error should name the failing node: %q", failed.Error)
	}

	// Ordering on the first attempt.
	for _, n := range []string{"cp1", "cp2", "cp3", "w1", "w2"} {
		if i := fc.index(n + ":packages"); i < 0 || i > fc.index("cp1:init") {
			t.Fatalf("%s packages must finish before kubeadm init", n)
		}
	}
	if !(fc.index("cp1:init") < fc.index("cp1:cni") && fc.index("cp1:cni") < fc.index("cp2:join") &&
		fc.index("cp2:join") < fc.index("cp3:join") && fc.index("cp3:join") < fc.index("w1:join")) {
		t.Fatalf("unexpected order: %v", fc.log)
	}
	if fc.count("w1:images") != 0 || fc.count("cp2:images") != 1 {
		t.Fatal("control-plane images are pulled only on control planes")
	}

	if err := engine.Retry(ctx, job.ID); err != nil {
		t.Fatal(err)
	}
	waitJob(t, db, job.ID, store.JobSucceeded)

	// Resume: finished steps are skipped, failed and always-run steps rerun.
	for entry, want := range map[string]int{"cp1:swap": 1, "w2:packages": 1, "cp1:init": 1, "cp1:cni": 1, "cp2:join": 1, "w1:join": 1,
		"w2:join": 2, "cp1:join-credentials": 2, "cp1:metrics-server": 1} {
		if got := fc.count(entry); got != want {
			t.Errorf("%s ran %d times, want %d (log %v)", entry, got, want, fc.log)
		}
	}
	got, _ := db.GetCluster(ctx, "c1")
	if got.Status != store.ClusterReady || got.APIServerURL != "https://192.168.10.50:6443" || got.KubeconfigSecretID == nil {
		t.Fatalf("cluster not finalized: %+v", got)
	}
	kc, err := sec.Get(ctx, *got.KubeconfigSecretID)
	if err != nil || !strings.Contains(string(kc), "kind: Config") {
		t.Fatalf("kubeconfig not stored encrypted: %v", err)
	}
	// The join token is registered for redaction on every session.
	for _, r := range fc.redactors {
		if out := r.String("token abcdef.0123456789abcdef"); out == "token abcdef.0123456789abcdef" {
			t.Fatal("pattern redaction missing")
		}
	}
	engine.Hub().Flush()
	logs, _ := db.ListLogs(ctx, job.ID, 0, 0)
	for _, l := range logs {
		if strings.Contains(l.Line, "c2VjcmV0") {
			t.Fatal("admin.conf content leaked into the job log")
		}
	}
}

func TestSpecForRejectsMemberWithoutRole(t *testing.T) {
	cid := "c"
	_, err := SpecFor(&store.Cluster{Name: "x", KubernetesVersion: "v1.37.2", PodCIDR: "10.244.0.0/16", ServiceCIDR: "10.96.0.0/12",
		CNI: "cilium", Topology: bootstrap.TopologySingle}, []store.Server{{ID: "a", Name: "a", Host: "10.0.0.1", ClusterID: &cid}})
	if err == nil || errors.Is(err, bootstrap.ErrInvalidSpec) {
		t.Fatalf("expected role error, got %v", err)
	}
}

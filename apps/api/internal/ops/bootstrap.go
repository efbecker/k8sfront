package ops

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/efbecker/k8sfront/apps/api/internal/bootstrap"
	"github.com/efbecker/k8sfront/apps/api/internal/jobs"
	"github.com/efbecker/k8sfront/apps/api/internal/k8sver"
	"github.com/efbecker/k8sfront/apps/api/internal/remote"
	"github.com/efbecker/k8sfront/apps/api/internal/sshx"
	"github.com/efbecker/k8sfront/apps/api/internal/store"
)

// KindBootstrap provisions a cluster with kubeadm on Debian 13+ nodes.
const KindBootstrap = "cluster.bootstrap"

// BootstrapParams are the job parameters.
type BootstrapParams struct {
	ClusterID string `json:"clusterId"`
}

// BootstrapResult is stored as the job result.
type BootstrapResult struct {
	APIServerURL string       `json:"apiServerUrl"`
	Nodes        []NodeStatus `json:"nodes"`
}

// NodeStatus is a node as reported by the Kubernetes API.
type NodeStatus struct {
	Name           string `json:"name"`
	Ready          bool   `json:"ready"`
	KubeletVersion string `json:"kubeletVersion"`
	OSImage        string `json:"osImage"`
	InternalIP     string `json:"internalIp"`
	ControlPlane   bool   `json:"controlPlane"`
}

// SpecFor builds the bootstrap spec of a cluster from its stored members.
// Control planes are ordered as stored (role, name, host): the first one runs
// kubeadm init.
func SpecFor(cl *store.Cluster, members []store.Server) (*bootstrap.Spec, error) {
	v, err := k8sver.Parse(cl.KubernetesVersion)
	if err != nil {
		return nil, err
	}
	opts, err := bootstrap.ParseOptions(cl.Options)
	if err != nil {
		return nil, fmt.Errorf("cluster options: %w", err)
	}
	spec := &bootstrap.Spec{ClusterName: cl.Name, Version: v, PodCIDR: cl.PodCIDR, ServiceCIDR: cl.ServiceCIDR,
		CNI: bootstrap.CNI(cl.CNI), Topology: cl.Topology, Options: opts}
	for _, s := range members {
		n := bootstrap.Node{ID: s.ID, Name: s.Name, Host: s.Host}
		switch s.Role {
		case store.RoleControlPlane:
			spec.ControlPlanes = append(spec.ControlPlanes, n)
		case store.RoleWorker:
			spec.Workers = append(spec.Workers, n)
		default:
			return nil, fmt.Errorf("server %s has no role in the cluster", s.Name)
		}
	}
	return spec, spec.Validate()
}

type bootstrapRun struct {
	o     *Ops
	jc    *jobs.Context
	spec  *bootstrap.Spec
	sec   bootstrap.JoinSecrets
	mu    sync.Mutex
	nodes map[string]Runner
}

func (b *bootstrapRun) runner(id string) Runner {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.nodes[id]
}

// exec runs a script on a node inside the given step, streaming its output.
func (b *bootstrapRun) exec(ctx context.Context, step *store.JobStep, n bootstrap.Node, script string) error {
	r := b.runner(n.ID)
	if r == nil {
		return fmt.Errorf("no session to %s", n.Name)
	}
	id := n.ID
	_, err := r.Run(ctx, script, sshx.RunOptions{Sudo: true}, b.jc.Sink(step, &id))
	return err
}

// nodeStep runs a resumable step on one node.
func (b *bootstrapRun) nodeStep(ctx context.Context, n bootstrap.Node, key, name string, script func() string) error {
	id := n.ID
	return b.jc.StepOnce(ctx, key, n.Name+": "+name, &id, func(ctx context.Context, st *store.JobStep) error {
		return b.exec(ctx, st, n, script())
	})
}

func (o *Ops) bootstrap(ctx context.Context, jc *jobs.Context) (err error) {
	var p BootstrapParams
	if err := jc.Params(&p); err != nil {
		return err
	}
	cl, err := o.Store.GetCluster(ctx, p.ClusterID)
	if err != nil {
		return fmt.Errorf("cluster %s: %w", p.ClusterID, err)
	}
	members, err := o.Store.ListClusterServers(ctx, cl.ID)
	if err != nil {
		return err
	}
	spec, err := SpecFor(cl, members)
	if err != nil {
		return err
	}
	o.setClusterStatus(cl.ID, store.ClusterProvisioning)
	defer func() {
		if err != nil {
			o.setClusterStatus(cl.ID, store.ClusterFailed)
		}
	}()

	sec, err := bootstrap.NewJoinSecrets()
	if err != nil {
		return err
	}
	b := &bootstrapRun{o: o, jc: jc, spec: spec, sec: sec, nodes: map[string]Runner{}}
	defer func() {
		for _, r := range b.nodes {
			r.Close()
		}
	}()

	jc.Logf(nil, "Cluster %s: Kubernetes %s, CNI %s %s, endpoint %s, %d control plane(s), %d worker(s)",
		spec.ClusterName, spec.Version, spec.CNI, spec.CNIVersion(), spec.Endpoint(), len(spec.ControlPlanes), len(spec.Workers))

	// 1. Prepare every node in parallel (connect + Debian 13 check + sudo, then
	//    swap, kernel, containerd, repository, packages, images).
	if err := b.prepareAll(ctx); err != nil {
		return err
	}
	primary := spec.ControlPlanes[0]

	// 2. kubeadm init on the first control plane.
	if err := b.nodeStep(ctx, primary, "init", "kubeadm init", func() string { return spec.InitScript(sec) }); err != nil {
		return err
	}
	if spec.Options.KubeVIP != nil && spec.Options.KubeVIP.Address != "" {
		if err := b.nodeStep(ctx, primary, "kube-vip-finalize", "kube-vip com admin.conf", bootstrap.KubeVIPFinalizeScript); err != nil {
			return err
		}
	}

	// 3. Store admin.conf (encrypted) and compute the CA hash for joins.
	var caHash string
	pid := primary.ID
	if err := jc.Step(ctx, "kubeconfig", primary.Name+": extrair admin.conf (cifrado)", &pid, func(ctx context.Context, _ *store.JobStep) error {
		var err error
		caHash, err = b.storeKubeconfig(ctx, cl.ID, primary)
		return err
	}); err != nil {
		return err
	}

	// 4. CNI.
	cniName := map[bootstrap.CNI]string{bootstrap.CNICilium: "Cilium", bootstrap.CNICalico: "Calico"}[spec.CNI]
	if err := b.nodeStep(ctx, primary, "cni", "CNI "+cniName+" "+spec.CNIVersion(), spec.CNIScript); err != nil {
		return err
	}

	// 5. Joins: control planes one at a time (etcd membership changes are
	//    serialized), then workers in parallel.
	if len(spec.ControlPlanes) > 1 || len(spec.Workers) > 0 {
		// Always runs: every attempt uses fresh, short-lived credentials.
		if err := jc.Step(ctx, "join-credentials", primary.Name+": credenciais de join (token 2h)", &pid, func(ctx context.Context, st *store.JobStep) error {
			return b.exec(ctx, st, primary, spec.JoinCredentialsScript(sec))
		}); err != nil {
			return err
		}
	}
	for _, n := range spec.ControlPlanes[1:] {
		n := n
		if err := b.nodeStep(ctx, n, "join", "kubeadm join (control plane)", func() string { return spec.JoinScript(n, true, sec, caHash) }); err != nil {
			return err
		}
	}
	if err := b.parallel(ctx, spec.Workers, func(ctx context.Context, n bootstrap.Node) error {
		return b.nodeStep(ctx, n, "join", "kubeadm join (worker)", func() string { return spec.JoinScript(n, false, sec, caHash) })
	}); err != nil {
		return err
	}

	// 6. Optional add-ons.
	if spec.Topology == bootstrap.TopologySingle || spec.Options.UntaintControlPlane {
		if err := b.nodeStep(ctx, primary, "untaint", "remover taint do control plane", bootstrap.UntaintScript); err != nil {
			return err
		}
	}
	if spec.Options.MetricsServer {
		if err := b.nodeStep(ctx, primary, "metrics-server", "metrics-server "+bootstrap.DefaultMetricsServer, bootstrap.MetricsServerScript); err != nil {
			return err
		}
	}

	// 7. Wait until every node reports Ready.
	var nodes []NodeStatus
	want := len(spec.ControlPlanes) + len(spec.Workers)
	if err := jc.Step(ctx, "ready", fmt.Sprintf("aguardar %d nó(s) Ready", want), nil, func(ctx context.Context, st *store.JobStep) error {
		var err error
		nodes, err = b.waitReady(ctx, st, primary, want)
		return err
	}); err != nil {
		return err
	}
	cl, err = o.Store.GetCluster(context.Background(), cl.ID)
	if err != nil {
		return err
	}
	cl.Status = store.ClusterReady
	if err := o.Store.UpdateCluster(context.Background(), cl); err != nil {
		return err
	}
	return jc.SetResult(context.Background(), BootstrapResult{APIServerURL: cl.APIServerURL, Nodes: nodes})
}

func (o *Ops) setClusterStatus(id, status string) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	cl, err := o.Store.GetCluster(ctx, id)
	if err != nil {
		return
	}
	cl.Status = status
	_ = o.Store.UpdateCluster(ctx, cl)
}

func allNodes(s *bootstrap.Spec) []bootstrap.Node {
	return append(append([]bootstrap.Node{}, s.ControlPlanes...), s.Workers...)
}

func (b *bootstrapRun) parallel(ctx context.Context, nodes []bootstrap.Node, fn func(context.Context, bootstrap.Node) error) error {
	var wg sync.WaitGroup
	var mu sync.Mutex
	var failed []string
	for _, n := range nodes {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if err := fn(ctx, n); err != nil {
				mu.Lock()
				failed = append(failed, n.Name)
				mu.Unlock()
			}
		}()
	}
	wg.Wait()
	if len(failed) > 0 {
		return fmt.Errorf("falha em %d nó(s): %s", len(failed), strings.Join(failed, ", "))
	}
	return ctx.Err()
}

func (b *bootstrapRun) prepareAll(ctx context.Context) error {
	isCP := map[string]bool{}
	for _, n := range b.spec.ControlPlanes {
		isCP[n.ID] = true
	}
	servers := map[string]*store.Server{}
	for _, n := range allNodes(b.spec) {
		s, err := b.o.Store.GetServer(ctx, n.ID)
		if err != nil {
			return err
		}
		servers[n.ID] = s
	}
	return b.parallel(ctx, allNodes(b.spec), func(ctx context.Context, n bootstrap.Node) error {
		id := n.ID
		err := b.jc.Step(ctx, "connect", n.Name+": SSH, Debian 13+ e sudo", &id, func(ctx context.Context, _ *store.JobStep) error {
			r, err := b.o.open(ctx, servers[n.ID], remote.Hooks{After: func(stage remote.Stage, detail string, err error) {
				if err == nil && detail != "" {
					b.jc.Logf(&id, "  %s", detail)
				}
			}})
			if err != nil {
				return err
			}
			r.Redactor().Add(b.sec.Token, b.sec.CertificateKey)
			b.mu.Lock()
			b.nodes[n.ID] = r
			b.mu.Unlock()
			return nil
		})
		if err != nil {
			return err
		}
		steps := []struct {
			key, name string
			script    func() string
		}{
			{"swap", "desativar swap", bootstrap.SwapScript},
			{"kernel", "módulos do kernel e sysctl", bootstrap.KernelScript},
			{"containerd", "containerd (SystemdCgroup)", bootstrap.ContainerdScript},
			{"k8s-repo", "repositório pkgs.k8s.io " + b.spec.Version.MinorString(), b.spec.RepoScript},
			{"k8s-packages", "kubelet, kubeadm, kubectl " + b.spec.Version.String() + " (hold)", b.spec.PackagesScript},
		}
		if isCP[n.ID] {
			steps = append(steps, struct {
				key, name string
				script    func() string
			}{"images", "imagens do control plane", b.spec.ImagesScript})
		}
		for _, st := range steps {
			if err := b.nodeStep(ctx, n, st.key, st.name, st.script); err != nil {
				return err
			}
		}
		return nil
	})
}

func (b *bootstrapRun) storeKubeconfig(ctx context.Context, clusterID string, primary bootstrap.Node) (string, error) {
	r := b.runner(primary.ID)
	ca, err := r.Run(ctx, bootstrap.ReadCACert, sshx.RunOptions{Sudo: true, Quiet: true}, nil)
	if err != nil {
		return "", fmt.Errorf("read ca.crt: %w", err)
	}
	caHash, err := bootstrap.CACertHash([]byte(ca.Stdout))
	if err != nil {
		return "", err
	}
	kc, err := r.Run(ctx, bootstrap.ReadAdminConf, sshx.RunOptions{Sudo: true, Quiet: true, CaptureLimit: 1 << 20}, nil)
	if err != nil {
		return "", fmt.Errorf("read admin.conf: %w", err)
	}
	if !strings.Contains(kc.Stdout, "kind: Config") {
		return "", errors.New("admin.conf has unexpected content")
	}
	if b.o.Secrets == nil {
		return "", errors.New("secrets manager not configured")
	}
	sid, err := b.o.Secrets.Put(ctx, store.SecretKubeconfig, []byte(kc.Stdout))
	if err != nil {
		return "", err
	}
	cl, err := b.o.Store.GetCluster(ctx, clusterID)
	if err != nil {
		return "", err
	}
	old := cl.KubeconfigSecretID
	cl.KubeconfigSecretID = &sid
	cl.APIServerURL = "https://" + b.spec.Endpoint()
	cl.ControlPlaneEndpoint = b.spec.Endpoint()
	if err := b.o.Store.UpdateCluster(ctx, cl); err != nil {
		return "", err
	}
	if old != nil {
		_ = b.o.Secrets.Delete(ctx, *old)
	}
	b.jc.Logf(&primary.ID, "  admin.conf armazenado cifrado (AES-256-GCM); API em %s; CA %s", cl.APIServerURL, caHash)
	return caHash, nil
}

type nodeList struct {
	Items []struct {
		Metadata struct {
			Name   string            `json:"name"`
			Labels map[string]string `json:"labels"`
		} `json:"metadata"`
		Status struct {
			Conditions []struct {
				Type   string `json:"type"`
				Status string `json:"status"`
			} `json:"conditions"`
			Addresses []struct {
				Type    string `json:"type"`
				Address string `json:"address"`
			} `json:"addresses"`
			NodeInfo struct {
				KubeletVersion string `json:"kubeletVersion"`
				OSImage        string `json:"osImage"`
			} `json:"nodeInfo"`
		} `json:"status"`
	} `json:"items"`
}

// ParseNodes converts `kubectl get nodes -o json` output.
func ParseNodes(raw string) ([]NodeStatus, error) {
	var nl nodeList
	if err := json.Unmarshal([]byte(raw), &nl); err != nil {
		return nil, fmt.Errorf("parse nodes: %w", err)
	}
	out := make([]NodeStatus, 0, len(nl.Items))
	for _, it := range nl.Items {
		ns := NodeStatus{Name: it.Metadata.Name, KubeletVersion: it.Status.NodeInfo.KubeletVersion, OSImage: it.Status.NodeInfo.OSImage}
		_, ns.ControlPlane = it.Metadata.Labels["node-role.kubernetes.io/control-plane"]
		for _, c := range it.Status.Conditions {
			if c.Type == "Ready" {
				ns.Ready = c.Status == "True"
			}
		}
		for _, a := range it.Status.Addresses {
			if a.Type == "InternalIP" {
				ns.InternalIP = a.Address
			}
		}
		out = append(out, ns)
	}
	return out, nil
}

func (b *bootstrapRun) waitReady(ctx context.Context, st *store.JobStep, primary bootstrap.Node, want int) ([]NodeStatus, error) {
	timeout, every := b.o.ReadyTimeout, b.o.PollInterval
	if timeout == 0 {
		timeout = 15 * time.Minute
	}
	if every == 0 {
		every = 10 * time.Second
	}
	deadline := time.Now().Add(timeout)
	sink := b.jc.Sink(st, nil)
	last := ""
	for {
		res, err := b.runner(primary.ID).Run(ctx, bootstrap.GetNodesJSON, sshx.RunOptions{Sudo: true, Quiet: true, CaptureLimit: 8 << 20}, nil)
		if err == nil {
			nodes, perr := ParseNodes(res.Stdout)
			if perr == nil {
				ready := 0
				var parts []string
				for _, n := range nodes {
					state := "NotReady"
					if n.Ready {
						ready++
						state = "Ready"
					}
					parts = append(parts, fmt.Sprintf("%s=%s(%s)", n.Name, state, n.KubeletVersion))
				}
				summary := fmt.Sprintf("%d/%d nó(s) Ready: %s", ready, want, strings.Join(parts, " "))
				if summary != last {
					sink(store.StreamSystem, summary)
					last = summary
				}
				if len(nodes) >= want && ready == len(nodes) {
					return nodes, nil
				}
			}
		}
		if time.Now().After(deadline) {
			return nil, fmt.Errorf("timeout de %s aguardando os nós ficarem Ready (%s)", timeout, last)
		}
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-time.After(every):
		}
	}
}

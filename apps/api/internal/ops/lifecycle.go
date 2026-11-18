package ops

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/efbecker/k8sfront/apps/api/internal/bootstrap"
	"github.com/efbecker/k8sfront/apps/api/internal/jobs"
	"github.com/efbecker/k8sfront/apps/api/internal/remote"
	"github.com/efbecker/k8sfront/apps/api/internal/sshx"
	"github.com/efbecker/k8sfront/apps/api/internal/store"
)

// Phase 5 job kinds (Updates & Lifecycle) and the phase 4 Helm job.
const (
	KindUpgrade       = "cluster.upgrade"
	KindNodeAdd       = "cluster.node-add"
	KindNodeRemove    = "cluster.node-remove"
	KindNodeDrain     = "cluster.node-drain"
	KindOSCheck       = "cluster.os-check"
	KindOSUpdate      = "cluster.os-update"
	KindRollingReboot = "cluster.rolling-reboot"
	KindCerts         = "cluster.certs"
	KindHelm          = "cluster.helm"
)

// LifecycleKinds are the jobs listed on the Updates & Lifecycle page.
var LifecycleKinds = []string{KindUpgrade, KindNodeAdd, KindNodeRemove, KindNodeDrain, KindOSCheck, KindOSUpdate, KindRollingReboot, KindCerts}

// lifecycleRun loads a provisioned cluster for a maintenance job.
func (o *Ops) lifecycleRun(ctx context.Context, jc *jobs.Context, clusterID string) (*bootstrapRun, *store.Cluster, error) {
	cl, err := o.Store.GetCluster(ctx, clusterID)
	if err != nil {
		return nil, nil, fmt.Errorf("cluster %s: %w", clusterID, err)
	}
	if cl.KubeconfigSecretID == nil {
		return nil, nil, errors.New("o cluster ainda não terminou o bootstrap")
	}
	members, err := o.Store.ListClusterServers(ctx, cl.ID)
	if err != nil {
		return nil, nil, err
	}
	spec, err := SpecFor(cl, members)
	if err != nil {
		return nil, nil, err
	}
	b := &bootstrapRun{o: o, jc: jc, spec: spec, nodes: map[string]Runner{}}
	for i := range members {
		b.mu.Lock()
		if b.servers == nil {
			b.servers = map[string]*store.Server{}
		}
		b.servers[members[i].ID] = &members[i]
		b.mu.Unlock()
	}
	return b, cl, nil
}

func (b *bootstrapRun) closeAll() {
	b.mu.Lock()
	defer b.mu.Unlock()
	for id, r := range b.nodes {
		r.Close()
		delete(b.nodes, id)
	}
}

func (b *bootstrapRun) primary() bootstrap.Node { return b.spec.ControlPlanes[0] }

func (b *bootstrapRun) node(id string) (bootstrap.Node, bool) {
	for _, n := range allNodes(b.spec) {
		if n.ID == id {
			return n, true
		}
	}
	return bootstrap.Node{}, false
}

// on runs script on the runOn node inside a step recorded for the `for` node
// (e.g. kubectl drain runs on the primary but belongs to the drained node).
func (b *bootstrapRun) on(ctx context.Context, step *store.JobStep, runOn, recordFor bootstrap.Node, script string) (*sshx.Result, error) {
	if err := b.connect(ctx, runOn); err != nil {
		return nil, err
	}
	id := recordFor.ID
	return b.runner(runOn.ID).Run(ctx, script, sshx.RunOptions{Sudo: true}, b.jc.Sink(step, &id))
}

// stepOn is a resumable step for node n executed on runOn.
func (b *bootstrapRun) stepOn(ctx context.Context, n, runOn bootstrap.Node, key, name string, script func() string) error {
	id := n.ID
	return b.jc.StepOnce(ctx, key, n.Name+": "+name, &id, func(ctx context.Context, st *store.JobStep) error {
		_, err := b.on(ctx, st, runOn, n, script())
		return err
	})
}

// quiet runs a read-only command and returns its stdout (never logged).
func (b *bootstrapRun) quiet(ctx context.Context, n bootstrap.Node, cmd string) (string, error) {
	if err := b.connect(ctx, n); err != nil {
		return "", err
	}
	res, err := b.runner(n.ID).Run(ctx, cmd, sshx.RunOptions{Sudo: true, Quiet: true, CaptureLimit: 8 << 20}, nil)
	if err != nil {
		return "", err
	}
	return res.Stdout, nil
}

// nodeNames maps server IDs to Kubernetes node names: by InternalIP first
// (kubeadm pins node-ip to the registered address), then by hostname.
func (b *bootstrapRun) nodeNames(ctx context.Context, nodes []bootstrap.Node) (map[string]string, error) {
	raw, err := b.quiet(ctx, b.primary(), bootstrap.GetNodesJSON)
	if err != nil {
		return nil, fmt.Errorf("listar nós: %w", err)
	}
	list, err := ParseNodes(raw)
	if err != nil {
		return nil, err
	}
	byIP, byName := map[string]string{}, map[string]bool{}
	for _, n := range list {
		if n.InternalIP != "" {
			byIP[n.InternalIP] = n.Name
		}
		byName[n.Name] = true
	}
	out := map[string]string{}
	for _, n := range nodes {
		if name, ok := byIP[n.Host]; ok {
			out[n.ID] = name
			continue
		}
		hn, err := b.quiet(ctx, n, bootstrap.ReadHostname)
		if err != nil {
			return nil, fmt.Errorf("%s: %w", n.Name, err)
		}
		hn = strings.ToLower(strings.TrimSpace(hn))
		if byName[hn] {
			out[n.ID] = hn
			continue
		}
		if i := strings.IndexByte(hn, '.'); i > 0 && byName[hn[:i]] {
			out[n.ID] = hn[:i]
			continue
		}
		return nil, fmt.Errorf("%s: nó não encontrado no Kubernetes (IP %s, hostname %s)", n.Name, n.Host, hn)
	}
	return out, nil
}

// resolveNames is nodeNames wrapped in a visible step.
func (b *bootstrapRun) resolveNames(ctx context.Context, nodes []bootstrap.Node) (map[string]string, error) {
	var names map[string]string
	pid := b.primary().ID
	err := b.jc.Step(ctx, "node-names", "identificar os nós no Kubernetes", &pid, func(ctx context.Context, st *store.JobStep) error {
		var err error
		names, err = b.nodeNames(ctx, nodes)
		if err == nil {
			var parts []string
			for _, n := range nodes {
				parts = append(parts, n.Name+"="+names[n.ID])
			}
			b.jc.Sink(st, &pid)(store.StreamSystem, "  "+strings.Join(parts, " "))
		}
		return err
	})
	return names, err
}

// maintain runs the rolling maintenance of one node: cordon + drain (on the
// primary) → action on the node → wait Ready → uncordon. The caller pauses the
// job on error (circuit breaker). If the action fails and the node is still
// Ready (e.g. apt could not download a package and the old kubelet keeps
// running), it is uncordoned so it does not sit idle while paused; that is
// why the drain is not a StepOnce: Retomar drains it again before retrying.
func (b *bootstrapRun) maintain(ctx context.Context, n bootstrap.Node, name, prefix string, single bool, action func(ctx context.Context) error, version string) error {
	p := b.primary()
	id := n.ID
	if done, err := b.jc.Completed(ctx, prefix+"uncordon", &id); err != nil {
		return err
	} else if done {
		b.jc.Logf(&id, "⏭ %s: manutenção já concluída numa tentativa anterior", n.Name)
		return nil
	}
	drainName := "cordon + drain (respeitando PDBs)"
	if single {
		drainName = "cordon"
	}
	if err := b.jc.Step(ctx, prefix+"drain", n.Name+": "+drainName, &id, func(ctx context.Context, st *store.JobStep) error {
		_, err := b.on(ctx, st, p, n, bootstrap.DrainScript(name, !single))
		return err
	}); err != nil {
		return err
	}
	if err := action(ctx); err != nil {
		b.restore(ctx, n, name, prefix)
		return err
	}
	waitName := "aguardar Ready"
	if version != "" {
		waitName = "aguardar Ready com kubelet " + version
	}
	if err := b.stepOn(ctx, n, p, prefix+"ready", waitName, func() string { return bootstrap.WaitNodeScript(name, version) }); err != nil {
		b.restore(ctx, n, name, prefix)
		return err
	}
	return b.stepOn(ctx, n, p, prefix+"uncordon", "uncordon", func() string { return bootstrap.UncordonScript(name) })
}

// restore puts a node whose maintenance failed back into service when it is
// still Ready (best effort; a failure here never hides the original error).
func (b *bootstrapRun) restore(ctx context.Context, n bootstrap.Node, name, prefix string) {
	if ctx.Err() != nil {
		return
	}
	id := n.ID
	_ = b.jc.Step(ctx, prefix+"restore", n.Name+": devolver ao serviço se estiver Ready", &id, func(ctx context.Context, st *store.JobStep) error {
		_, err := b.on(ctx, st, b.primary(), n, bootstrap.RestoreNodeScript(name))
		return err
	})
}

// rebootAndWait reboots the node, waits for SSH to come back with a new
// boot id (the Debian 13+ check runs again on reconnect) and for the kubelet.
func (b *bootstrapRun) rebootAndWait(ctx context.Context, n bootstrap.Node, key string) error {
	id := n.ID
	err := b.jc.StepOnce(ctx, key, n.Name+": reiniciar e aguardar SSH", &id, func(ctx context.Context, st *store.JobStep) error {
		sink := b.jc.Sink(st, &id)
		before, err := b.quiet(ctx, n, bootstrap.ReadBootID)
		if err != nil {
			return err
		}
		before = strings.TrimSpace(before)
		if _, err := b.on(ctx, st, n, n, bootstrap.RebootScript()); err != nil {
			return err
		}
		b.mu.Lock()
		if r := b.nodes[n.ID]; r != nil {
			r.Close()
			delete(b.nodes, n.ID)
		}
		b.mu.Unlock()
		srv, err := b.server(ctx, n.ID)
		if err != nil {
			return err
		}
		timeout := b.o.RebootTimeout
		if timeout == 0 {
			timeout = 15 * time.Minute
		}
		every := b.o.PollInterval
		if every == 0 {
			every = 10 * time.Second
		}
		deadline := time.Now().Add(timeout)
		attempt := 0
		for {
			select {
			case <-ctx.Done():
				return ctx.Err()
			case <-time.After(every):
			}
			attempt++
			octx, cancel := context.WithTimeout(ctx, 30*time.Second)
			r, err := b.o.open(octx, srv, remote.Hooks{})
			if err == nil {
				res, rerr := r.Run(octx, bootstrap.ReadBootID, sshx.RunOptions{Quiet: true}, nil)
				if rerr == nil && strings.TrimSpace(res.Stdout) != "" && strings.TrimSpace(res.Stdout) != before {
					cancel()
					b.mu.Lock()
					b.nodes[n.ID] = r
					b.mu.Unlock()
					sink(store.StreamSystem, fmt.Sprintf("  %s voltou (novo boot id) após %d tentativa(s)", n.Name, attempt))
					return nil
				}
				r.Close()
			}
			cancel()
			if attempt%6 == 0 {
				sink(store.StreamSystem, fmt.Sprintf("  aguardando %s voltar…", n.Name))
			}
			if time.Now().After(deadline) {
				return fmt.Errorf("%s não voltou em %s após o reboot", n.Name, timeout)
			}
		}
	})
	if err != nil {
		return err
	}
	return b.nodeStep(ctx, n, key+"-kubelet", "aguardar kubelet", bootstrap.WaitKubeletScript)
}

// orderedNodes returns control planes first (primary first), then workers,
// optionally restricted to ids.
func (b *bootstrapRun) orderedNodes(ids []string) []bootstrap.Node {
	want := map[string]bool{}
	for _, id := range ids {
		want[id] = true
	}
	var out []bootstrap.Node
	for _, n := range allNodes(b.spec) {
		if len(want) == 0 || want[n.ID] {
			out = append(out, n)
		}
	}
	return out
}

func pause(n bootstrap.Node, err error) error {
	if err == nil {
		return nil
	}
	if errors.Is(err, context.Canceled) {
		return err
	}
	return jobs.Pause(fmt.Errorf("%s: %w (job pausado antes do próximo nó; corrija e use Retomar)", n.Name, err))
}

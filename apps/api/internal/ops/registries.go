package ops

import (
	"context"
	"errors"
	"fmt"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/efbecker/k8sfront/apps/api/internal/bootstrap"
	"github.com/efbecker/k8sfront/apps/api/internal/jobs"
	"github.com/efbecker/k8sfront/apps/api/internal/kube"
	"github.com/efbecker/k8sfront/apps/api/internal/registry"
	"github.com/efbecker/k8sfront/apps/api/internal/store"
)

// Registry and image job kinds.
const (
	KindRegistriesSync = "cluster.registries-sync"
	KindImagePrepull   = "cluster.image-prepull"
	KindImagePrune     = "cluster.image-prune"
)

// ImageKinds are the jobs listed on the Images page.
var ImageKinds = []string{KindRegistriesSync, KindImagePrepull, KindImagePrune}

// KubeClients resolves the client-go clients of a cluster (*kube.Manager).
type KubeClients interface {
	Get(ctx context.Context, clusterID string) (*kube.Cluster, error)
}

func (o *Ops) kube(ctx context.Context, clusterID string) (*kube.Cluster, error) {
	if o.Kube == nil {
		return nil, errors.New("cliente Kubernetes não configurado")
	}
	return o.Kube.Get(ctx, clusterID)
}

// RegistriesSyncResult summarizes a sync (no secret data).
type RegistriesSyncResult struct {
	Registries  []string                `json:"registries"`
	Nodes       int                     `json:"nodes"`
	PullSecrets []kube.PullSecretChange `json:"pullSecrets"`
}

// RegistryHosts renders the containerd configuration of the registries
// targeting the cluster.
func RegistryHosts(regs []store.Registry, clusterID string) ([]bootstrap.RegistryHost, []string) {
	var hosts []bootstrap.RegistryHost
	var names []string
	for i := range regs {
		r := &regs[i]
		if !r.AppliesTo(clusterID) {
			continue
		}
		caPath := ""
		if r.CACert != "" {
			caPath = "/etc/containerd/certs.d/" + registry.HostDir(r) + "/ca.crt"
		}
		hosts = append(hosts, bootstrap.RegistryHost{Dir: registry.HostDir(r), TOML: registry.HostsTOML(r, caPath), CA: r.CACert})
		names = append(names, r.Name)
	}
	return hosts, names
}

// registriesSync writes the containerd registry configuration on every node,
// one node at a time (containerd restarts once when config_path is first
// enabled; a failure pauses before the next node), then reconciles the
// imagePullSecrets of every namespace.
func (o *Ops) registriesSync(ctx context.Context, jc *jobs.Context) error {
	var p ClusterParams
	if err := jc.Params(&p); err != nil {
		return err
	}
	b, cl, err := o.lifecycleRun(ctx, jc, p.ClusterID)
	if err != nil {
		return err
	}
	defer b.closeAll()
	regs, err := o.Store.ListRegistries(ctx)
	if err != nil {
		return err
	}
	hosts, names := RegistryHosts(regs, cl.ID)
	if len(names) == 0 {
		jc.Logf(nil, "Nenhum registry cadastrado para este cluster: configurações antigas do k8sfront serão removidas")
	} else {
		jc.Logf(nil, "Registries: %s", strings.Join(names, ", "))
	}
	script := bootstrap.RegistriesScript(hosts)
	nodes := b.orderedNodes(nil)
	for _, n := range nodes {
		if err := b.connect(ctx, n); err != nil {
			return pause(n, err)
		}
		id := n.ID
		if err := jc.Step(ctx, "registries", n.Name+": containerd (certs.d/hosts.toml)", &id, func(ctx context.Context, st *store.JobStep) error {
			_, err := b.on(ctx, st, n, n, script)
			return err
		}); err != nil {
			return pause(n, err)
		}
	}
	res := RegistriesSyncResult{Registries: names, Nodes: len(nodes), PullSecrets: []kube.PullSecretChange{}}
	err = jc.Step(ctx, "pull-secrets", "imagePullSecrets nos namespaces", nil, func(ctx context.Context, st *store.JobStep) error {
		changes, err := o.syncPullSecrets(ctx, cl.ID, "")
		res.PullSecrets = changes
		sink := jc.Sink(st, nil)
		failed := 0
		for _, c := range changes {
			line := fmt.Sprintf("  %s/%s: %s", c.Namespace, c.Name, map[string]string{"applied": "aplicado", "removed": "removido", "error": "erro"}[c.Action])
			if c.Error != "" {
				failed++
				line += " (" + c.Error + ")"
			}
			sink(store.StreamSystem, line)
		}
		if len(changes) == 0 {
			sink(store.StreamSystem, "  nenhum imagePullSecret a aplicar (registries sem credenciais ou com Secrets desativados)")
		}
		if err == nil && failed > 0 {
			err = fmt.Errorf("%d imagePullSecret(s) com erro", failed)
		}
		return err
	})
	if rerr := jc.SetResult(context.Background(), res); rerr != nil && err == nil {
		err = rerr
	}
	return err
}

func (o *Ops) syncPullSecrets(ctx context.Context, clusterID, only string) ([]kube.PullSecretChange, error) {
	if o.Secrets == nil {
		return nil, errors.New("secrets manager not configured")
	}
	kc, err := o.kube(ctx, clusterID)
	if err != nil {
		return nil, err
	}
	specs, err := kube.DesiredPullSecrets(ctx, o.Store, o.Secrets, clusterID)
	if err != nil {
		return nil, err
	}
	return kube.SyncPullSecrets(ctx, kc.Client, kc.Metadata, specs, only)
}

// PrepullParams lists the images to cache on every node.
type PrepullParams struct {
	ClusterID string   `json:"clusterId"`
	Images    []string `json:"images"`
}

// PrepullResult is the pull state of each image on each node.
type PrepullResult struct {
	Images []string         `json:"images"`
	States []kube.PullState `json:"states"`
}

// PrepullTimeout bounds the wait for every node to pull every image.
const PrepullTimeout = 15 * time.Minute

// imagePrepull caches images on every node through a temporary DaemonSet in
// k8sfront-system: kubelet pulls with the registries' imagePullSecrets (no
// credential ever reaches a node command line), and the bogus command keeps
// the image's entrypoint from running.
func (o *Ops) imagePrepull(ctx context.Context, jc *jobs.Context) error {
	var p PrepullParams
	if err := jc.Params(&p); err != nil {
		return err
	}
	if len(p.Images) == 0 || len(p.Images) > 20 {
		return errors.New("informe de 1 a 20 imagens")
	}
	for _, im := range p.Images {
		if _, err := registry.ParseImage(im); err != nil {
			return err
		}
	}
	kc, err := o.kube(ctx, p.ClusterID)
	if err != nil {
		return err
	}
	cs := kc.Client
	ns := kube.PrepullNamespace
	var secretNames []string
	if err := jc.Step(ctx, "prepare", "namespace "+ns+" e imagePullSecrets", nil, func(ctx context.Context, st *store.JobStep) error {
		if err := kube.EnsurePrepullNamespace(ctx, cs); err != nil {
			return err
		}
		specs, err := kube.DesiredPullSecrets(ctx, o.Store, o.Secrets, p.ClusterID)
		if err != nil {
			return err
		}
		for i := range specs {
			specs[i].Namespaces = []string{ns}
			secretNames = append(secretNames, specs[i].Name)
		}
		changes, err := kube.SyncPullSecrets(ctx, cs, kc.Metadata, specs, ns)
		for _, c := range changes {
			if c.Error != "" {
				return fmt.Errorf("%s: %s", c.Name, c.Error)
			}
		}
		jc.Sink(st, nil)(store.StreamSystem, fmt.Sprintf("  %d imagePullSecret(s) disponíveis para o pull", len(secretNames)))
		return err
	}); err != nil {
		return err
	}
	name := "prepull-" + strings.ToLower(jc.Job.ID[:8])
	cleanup := func() {
		cctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		kube.StopPrepull(cctx, cs, name)
	}
	if err := jc.Step(ctx, "daemonset", fmt.Sprintf("DaemonSet temporário com %d imagem(ns)", len(p.Images)), nil, func(ctx context.Context, st *store.JobStep) error {
		return kube.StartPrepull(ctx, cs, name, p.Images, secretNames)
	}); err != nil {
		return err
	}
	defer cleanup()
	res := PrepullResult{Images: p.Images}
	every := o.PollInterval
	if every == 0 {
		every = 3 * time.Second
	}
	err = jc.Step(ctx, "pull", "aguardar o pull em todos os nós", nil, func(ctx context.Context, st *store.JobStep) error {
		sink := jc.Sink(st, nil)
		deadline := time.Now().Add(PrepullTimeout)
		last := ""
		for {
			nodes, states, err := kube.PrepullProgress(ctx, cs, name, p.Images)
			if err != nil {
				return err
			}
			done, failed, pending := 0, 0, 0
			for _, s := range states {
				switch s.State {
				case kube.PullDone:
					done++
				case kube.PullFailed:
					failed++
				default:
					pending++
				}
			}
			want := nodes * len(p.Images)
			summary := fmt.Sprintf("  %d/%d pull(s) concluído(s), %d com erro, %d nó(s)", done, want, failed, nodes)
			if summary != last {
				sink(store.StreamSystem, summary)
				last = summary
			}
			res.States = states
			if want > 0 && len(states) >= want && pending == 0 {
				sort.Slice(res.States, func(i, j int) bool {
					return res.States[i].Node+res.States[i].Image < res.States[j].Node+res.States[j].Image
				})
				for _, s := range res.States {
					if s.State == kube.PullFailed {
						sink(store.StreamStderr, fmt.Sprintf("  %s em %s: %s", s.Image, s.Node, s.Message))
					}
				}
				if failed > 0 {
					return fmt.Errorf("%d pull(s) falharam (veja as mensagens acima)", failed)
				}
				return nil
			}
			if time.Now().After(deadline) {
				return fmt.Errorf("tempo esgotado (%s) com %d pull(s) pendentes", PrepullTimeout, max(pending, want-done-failed))
			}
			select {
			case <-ctx.Done():
				return ctx.Err()
			case <-time.After(every):
			}
		}
	})
	if rerr := jc.SetResult(context.Background(), res); rerr != nil && err == nil {
		err = rerr
	}
	return err
}

// PruneParams: ServerIDs empty means every node.
type PruneParams struct {
	ClusterID string   `json:"clusterId"`
	ServerIDs []string `json:"serverIds,omitempty"`
}

// PruneNode is the cleanup outcome on one node.
type PruneNode struct {
	ServerID   string `json:"serverId"`
	Name       string `json:"name"`
	Removed    int    `json:"removed"`
	Remaining  int    `json:"remaining"`
	FreedBytes int64  `json:"freedBytes"`
	Error      string `json:"error,omitempty"`
}

var pruneLine = regexp.MustCompile(`K8SFRONT_PRUNE removed=(-?\d+) remaining=(\d+) freed=(\d+)`)

// imagePrune removes the images no container uses (crictl rmi --prune) on
// each node, in parallel: running workloads are not touched.
func (o *Ops) imagePrune(ctx context.Context, jc *jobs.Context) error {
	var p PruneParams
	if err := jc.Params(&p); err != nil {
		return err
	}
	b, _, err := o.lifecycleRun(ctx, jc, p.ClusterID)
	if err != nil {
		return err
	}
	defer b.closeAll()
	nodes := b.orderedNodes(p.ServerIDs)
	out := make([]PruneNode, len(nodes))
	var wg sync.WaitGroup
	for i, n := range nodes {
		wg.Add(1)
		go func() {
			defer wg.Done()
			id := n.ID
			r := PruneNode{ServerID: n.ID, Name: n.Name}
			err := jc.Step(ctx, "prune", n.Name+": remover imagens sem uso (crictl rmi --prune)", &id, func(ctx context.Context, st *store.JobStep) error {
				res, err := b.on(ctx, st, n, n, bootstrap.ImagePruneScript())
				if err != nil {
					return err
				}
				if m := pruneLine.FindStringSubmatch(res.Stdout); m != nil {
					r.Removed, _ = strconv.Atoi(m[1])
					r.Remaining, _ = strconv.Atoi(m[2])
					r.FreedBytes, _ = strconv.ParseInt(m[3], 10, 64)
				}
				return nil
			})
			if err != nil {
				r.Error = err.Error()
			}
			out[i] = r
		}()
	}
	wg.Wait()
	if err := jc.SetResult(context.Background(), map[string]any{"nodes": out}); err != nil {
		return err
	}
	var failed []string
	for _, r := range out {
		if r.Error != "" {
			failed = append(failed, r.Name)
		}
	}
	if len(failed) > 0 {
		return fmt.Errorf("falha em %d nó(s): %s", len(failed), strings.Join(failed, ", "))
	}
	return nil
}

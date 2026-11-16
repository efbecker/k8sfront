package ops

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/efbecker/k8sfront/apps/api/internal/bootstrap"
	"github.com/efbecker/k8sfront/apps/api/internal/jobs"
	"github.com/efbecker/k8sfront/apps/api/internal/store"
)

// NodeParams target one server of the cluster.
type NodeParams struct {
	ClusterID string `json:"clusterId"`
	ServerID  string `json:"serverId"`
	// DeleteServer also removes the server from the inventory at the end
	// (the "Remover servidor" action on the Servers page).
	DeleteServer bool `json:"deleteServer,omitempty"`
}

// DrainParams target a Kubernetes node by name.
type DrainParams struct {
	ClusterID string `json:"clusterId"`
	Node      string `json:"node"`
}

// isControlPlane reports whether id is one of the spec's control planes.
func (b *bootstrapRun) isControlPlane(id string) bool {
	for _, cp := range b.spec.ControlPlanes {
		if cp.ID == id {
			return true
		}
	}
	return false
}

// avoidPrimary makes sure the job's primary (the control plane that runs
// kubectl and issues join credentials) is not the node being added or
// removed: the stored order is by name, so a new control plane may sort first.
func (b *bootstrapRun) avoidPrimary(id string) {
	cps := b.spec.ControlPlanes
	if len(cps) > 1 && cps[0].ID == id {
		b.spec.ControlPlanes = append(append([]bootstrap.Node{}, cps[1:]...), cps[0])
	}
}

// others returns every node of the spec except id.
func (b *bootstrapRun) others(id string) []bootstrap.Node {
	var out []bootstrap.Node
	for _, n := range allNodes(b.spec) {
		if n.ID != id {
			out = append(out, n)
		}
	}
	return out
}

// controlPlanesWithout returns the control planes except id.
func (b *bootstrapRun) controlPlanesWithout(id string) []bootstrap.Node {
	var out []bootstrap.Node
	for _, n := range b.spec.ControlPlanes {
		if n.ID != id {
			out = append(out, n)
		}
	}
	return out
}

// nodeAdd joins a new worker or control plane (already attached to the
// cluster by the API with its role): preparation, the local API proxy of the
// other nodes learns the new control plane, fresh join credentials on the
// primary, kubeadm join, Ready.
func (o *Ops) nodeAdd(ctx context.Context, jc *jobs.Context) error {
	var p NodeParams
	if err := jc.Params(&p); err != nil {
		return err
	}
	b, _, err := o.lifecycleRun(ctx, jc, p.ClusterID)
	if err != nil {
		return err
	}
	defer b.closeAll()
	n, ok := b.node(p.ServerID)
	if !ok {
		return fmt.Errorf("o servidor %s não pertence ao cluster", p.ServerID)
	}
	controlPlane := b.isControlPlane(n.ID)
	if controlPlane {
		if len(b.spec.ControlPlanes) < 2 {
			return errors.New("o cluster não tem outro control plane para receber o novo")
		}
		if b.spec.Mode() == bootstrap.EndpointDirect {
			return errors.New("este cluster usa o IP do primeiro control plane como endpoint da API; adicionar control planes exige o proxy local ou um VIP (recrie o cluster)")
		}
		b.avoidPrimary(n.ID)
	}
	role := "worker"
	if controlPlane {
		role = "control plane"
	}
	jc.Logf(nil, "Adicionando %s como %s (endpoint %s, %s)", n.Name, role, b.spec.Endpoint(), endpointLabel(b.spec.Mode()))
	if b.sec, err = bootstrap.NewJoinSecrets(); err != nil {
		return err
	}
	primary := b.primary()
	if err := b.connect(ctx, primary); err != nil {
		return err
	}
	b.runner(primary.ID).Redactor().Add(b.sec.Token, b.sec.CertificateKey)
	if err := b.prepare(ctx, []bootstrap.Node{n}); err != nil {
		return err
	}
	if controlPlane {
		// Harmless before the join: the new backend fails its health check
		// until its kube-apiserver is ready.
		if err := b.syncAPIProxy(ctx, b.others(n.ID), b.spec.ControlPlanes, "apiproxy-add"); err != nil {
			return err
		}
	}
	pid := primary.ID
	if err := jc.Step(ctx, "join-credentials", primary.Name+": credenciais de join (token 2h)", &pid, func(ctx context.Context, st *store.JobStep) error {
		return b.exec(ctx, st, primary, b.spec.JoinCredentialsScript(b.sec))
	}); err != nil {
		return err
	}
	caHash, err := b.caHash(ctx, primary)
	if err != nil {
		return err
	}
	if err := b.nodeStep(ctx, n, "join", "kubeadm join ("+role+")", func() string { return b.spec.JoinScript(n, controlPlane, b.sec, caHash) }); err != nil {
		return err
	}
	names, err := b.resolveNames(ctx, []bootstrap.Node{n})
	if err != nil {
		return err
	}
	if err := b.stepOn(ctx, n, primary, "ready", "aguardar Ready", func() string { return bootstrap.WaitNodeScript(names[n.ID], "") }); err != nil {
		return err
	}
	if controlPlane {
		o.refreshAPIServerURL(p.ClusterID)
		if c := len(b.spec.ControlPlanes); c%2 == 0 {
			jc.Logf(nil, "⚠ o cluster tem %d control planes: com número par o etcd tolera as mesmas falhas que com %d; adicione mais um", c, c-1)
		}
	}
	return nil
}

// refreshAPIServerURL recomputes the address k8sfront shows for the API after
// the control planes changed (the kube client fails over between all of them).
func (o *Ops) refreshAPIServerURL(clusterID string) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	cl, err := o.Store.GetCluster(ctx, clusterID)
	if err != nil {
		return
	}
	members, err := o.Store.ListClusterServers(ctx, clusterID)
	if err != nil {
		return
	}
	spec, err := SpecFor(cl, members)
	if err != nil || len(spec.ControlPlanes) == 0 {
		return
	}
	if url := "https://" + spec.ExternalEndpoints()[0]; url != cl.APIServerURL {
		cl.APIServerURL = url
		_ = o.Store.UpdateCluster(ctx, cl)
	}
}

// nodeRemove drains and deletes a worker or control plane, resets kubeadm on
// it (best effort) and detaches the server from the cluster. A control plane
// first leaves the local API proxy of the other nodes and its etcd member is
// removed from a remaining control plane even if the server is unreachable.
func (o *Ops) nodeRemove(ctx context.Context, jc *jobs.Context) error {
	var p NodeParams
	if err := jc.Params(&p); err != nil {
		return err
	}
	b, _, err := o.lifecycleRun(ctx, jc, p.ClusterID)
	if err != nil {
		return err
	}
	defer b.closeAll()
	n, ok := b.node(p.ServerID)
	if !ok {
		return fmt.Errorf("o servidor %s não pertence ao cluster", p.ServerID)
	}
	controlPlane := b.isControlPlane(n.ID)
	if controlPlane {
		if len(b.spec.ControlPlanes) < 2 {
			return errors.New("não é possível remover o único control plane do cluster")
		}
		b.avoidPrimary(n.ID)
	}
	primary := b.primary()
	if err := b.connect(ctx, primary); err != nil {
		return err
	}
	name := ""
	if names, err := b.nodeNames(ctx, []bootstrap.Node{n}); err == nil {
		name = names[n.ID]
	} else {
		jc.Logf(&n.ID, "  %v: o nó não aparece no Kubernetes; pulando drain e delete", err)
	}
	if name != "" {
		if err := b.stepOn(ctx, n, primary, "drain", "cordon + drain (respeitando PDBs)", func() string { return bootstrap.DrainScript(name, true) }); err != nil {
			return pause(n, err)
		}
	}
	if controlPlane {
		if err := b.syncAPIProxy(ctx, b.others(n.ID), b.controlPlanesWithout(n.ID), "apiproxy-remove"); err != nil {
			return pause(n, err)
		}
	}
	if name != "" && !controlPlane {
		if err := b.stepOn(ctx, n, primary, "delete-node", "kubectl delete node "+name, func() string { return bootstrap.DeleteNodeScript(name) }); err != nil {
			return err
		}
	}
	id := n.ID
	if err := jc.Step(ctx, "reset", n.Name+": kubeadm reset", &id, func(ctx context.Context, st *store.JobStep) error {
		if err := b.connect(ctx, n); err != nil {
			jc.Logf(&id, "  aviso: sem acesso ao servidor (%v); rode 'kubeadm reset -f' nele manualmente", err)
			return nil
		}
		return b.exec(ctx, st, n, bootstrap.ResetNodeScript())
	}); err != nil {
		return err
	}
	if controlPlane {
		// kubeadm reset removes its own etcd member when it can; this makes
		// sure, from a remaining control plane, that the member is gone.
		if err := b.stepOn(ctx, n, primary, "etcd-member", "remover membro do etcd", func() string { return bootstrap.EtcdMemberRemoveScript(name, n.Host) }); err != nil {
			return pause(n, err)
		}
		if name != "" {
			if err := b.stepOn(ctx, n, primary, "delete-node", "kubectl delete node "+name, func() string { return bootstrap.DeleteNodeScript(name) }); err != nil {
				return err
			}
		}
	}
	srv, err := o.Store.GetServer(context.Background(), n.ID)
	if err != nil {
		return err
	}
	srv.ClusterID = nil
	if err := o.Store.UpdateServer(context.Background(), srv); err != nil {
		return err
	}
	if controlPlane {
		o.refreshAPIServerURL(p.ClusterID)
		if c := len(b.spec.ControlPlanes) - 1; c > 1 && c%2 == 0 {
			jc.Logf(nil, "⚠ restaram %d control planes: com número par o etcd tolera as mesmas falhas que com %d", c, c-1)
		}
	}
	if p.DeleteServer {
		if err := o.Store.DeleteServer(context.Background(), srv.ID); err != nil {
			return fmt.Errorf("nó removido do cluster, mas não foi possível apagar o servidor do inventário: %w", err)
		}
		jc.Logf(nil, "✔ %s removido do cluster e do inventário", n.Name)
		return nil
	}
	jc.Logf(nil, "✔ %s removido do cluster e liberado no inventário", n.Name)
	return nil
}

// nodeDrain cordons and drains a node by its Kubernetes name.
func (o *Ops) nodeDrain(ctx context.Context, jc *jobs.Context) error {
	var p DrainParams
	if err := jc.Params(&p); err != nil {
		return err
	}
	b, _, err := o.lifecycleRun(ctx, jc, p.ClusterID)
	if err != nil {
		return err
	}
	defer b.closeAll()
	primary := b.primary()
	pid := primary.ID
	return jc.Step(ctx, "drain", "cordon + drain de "+p.Node+" (respeitando PDBs)", &pid, func(ctx context.Context, st *store.JobStep) error {
		_, err := b.on(ctx, st, primary, primary, bootstrap.DrainScript(p.Node, true))
		return err
	})
}

package ops

import (
	"context"
	"errors"
	"fmt"

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

// nodeAdd joins a new worker (already attached to the cluster by the API):
// preparation, fresh join credentials on the primary, kubeadm join, Ready.
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
	for _, cp := range b.spec.ControlPlanes {
		if cp.ID == n.ID {
			return errors.New("adicionar control planes não é suportado; adicione workers")
		}
	}
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
	if err := b.nodeStep(ctx, n, "join", "kubeadm join (worker)", func() string { return b.spec.JoinScript(n, false, b.sec, caHash) }); err != nil {
		return err
	}
	names, err := b.resolveNames(ctx, []bootstrap.Node{n})
	if err != nil {
		return err
	}
	return b.stepOn(ctx, n, primary, "ready", "aguardar Ready", func() string { return bootstrap.WaitNodeScript(names[n.ID], "") })
}

// nodeRemove drains and deletes a worker, resets kubeadm on it (best effort)
// and detaches the server from the cluster.
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
	for _, cp := range b.spec.ControlPlanes {
		if cp.ID == n.ID {
			return errors.New("remover control planes não é suportado (quórum do etcd); remova workers")
		}
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
	srv, err := o.Store.GetServer(context.Background(), n.ID)
	if err != nil {
		return err
	}
	srv.ClusterID = nil
	if err := o.Store.UpdateServer(context.Background(), srv); err != nil {
		return err
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

package ops

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/efbecker/k8sfront/apps/api/internal/bootstrap"
	"github.com/efbecker/k8sfront/apps/api/internal/jobs"
	"github.com/efbecker/k8sfront/apps/api/internal/k8sver"
	"github.com/efbecker/k8sfront/apps/api/internal/store"
)

// UpgradeParams: the hops come from the upgrade plan (one per minor, never
// skipping one); the last hop is the target.
type UpgradeParams struct {
	ClusterID string   `json:"clusterId"`
	From      string   `json:"from"`
	Hops      []string `json:"hops"`
}

// UpgradeResult is stored as the job result.
type UpgradeResult struct {
	From string   `json:"from"`
	To   string   `json:"to"`
	Hops []string `json:"hops"`
}

// ValidateHops checks that hops only move forward, one minor at a time.
func ValidateHops(from string, hops []string) ([]k8sver.Version, error) {
	cur, err := k8sver.Parse(from)
	if err != nil {
		return nil, err
	}
	if len(hops) == 0 {
		return nil, errors.New("nenhuma versão de destino")
	}
	var out []k8sver.Version
	for _, h := range hops {
		v, err := k8sver.Parse(h)
		if err != nil {
			return nil, err
		}
		if v.Major != cur.Major || v.Compare(cur) <= 0 || v.Minor > cur.Minor+1 {
			return nil, fmt.Errorf("salto inválido %s → %s (um minor por vez, sempre para frente)", cur, v)
		}
		out = append(out, v)
		cur = v
	}
	return out, nil
}

// upgrade walks the hops. Per hop: etcd snapshot, then the primary control
// plane (kubeadm upgrade apply), the other control planes and the workers one
// node at a time (kubeadm upgrade node): repository → kubeadm → cordon/drain →
// kubelet/kubectl → Ready with the new version → uncordon. Any failure pauses
// the job before the next node.
func (o *Ops) upgrade(ctx context.Context, jc *jobs.Context) error {
	var p UpgradeParams
	if err := jc.Params(&p); err != nil {
		return err
	}
	hops, err := ValidateHops(p.From, p.Hops)
	if err != nil {
		return err
	}
	b, _, err := o.lifecycleRun(ctx, jc, p.ClusterID)
	if err != nil {
		return err
	}
	defer b.closeAll()
	nodes := b.orderedNodes(nil)
	primary := b.primary()
	single := len(nodes) == 1
	jc.Logf(nil, "Upgrade do Kubernetes %s → %s (%s), %d nó(s), um por vez", p.From, hops[len(hops)-1], strings.Join(p.Hops, " → "), len(nodes))

	pid := primary.ID
	if err := jc.StepOnce(ctx, "precheck", "pré-checagens: nós Ready, etcd, APIs depreciadas, PDBs", &pid, func(ctx context.Context, st *store.JobStep) error {
		_, err := b.on(ctx, st, primary, primary, bootstrap.UpgradePrecheckScript())
		return err
	}); err != nil {
		return jobs.Pause(fmt.Errorf("pré-checagem falhou: %w", err))
	}
	names, err := b.resolveNames(ctx, nodes)
	if err != nil {
		return err
	}

	for _, v := range hops {
		tag := "upgrade-" + v.String() + ":"
		if err := b.retryNode(ctx, primary, func(ctx context.Context, _ bool) error {
			return b.stepOn(ctx, primary, primary, tag+"etcd-snapshot", "snapshot do etcd antes de "+v.String(), func() string {
				return bootstrap.EtcdSnapshotScript(names[primary.ID], "pre-"+v.String())
			})
		}); err != nil {
			return pause(primary, err)
		}
		for _, n := range nodes {
			apply := n.ID == primary.ID
			kubeadmName := "kubeadm " + v.String() + " + kubeadm upgrade node"
			if apply {
				kubeadmName = "kubeadm " + v.String() + " + kubeadm upgrade apply"
			}
			err := b.retryNode(ctx, n, func(ctx context.Context, final bool) error {
				err := b.stepOn(ctx, n, n, tag+"repo", "repositório pkgs.k8s.io "+v.MinorString(), func() string { return bootstrap.RepoScriptFor(v) })
				if err == nil {
					err = b.stepOn(ctx, n, n, tag+"kubeadm", kubeadmName, func() string { return bootstrap.UpgradeKubeadmScript(v, apply) })
				}
				if err == nil {
					err = b.maintain(ctx, n, names[n.ID], tag, single, final, func(ctx context.Context) error {
						return b.stepOn(ctx, n, n, tag+"kubelet", "kubelet e kubectl "+v.String()+" (unhold → install → hold)", func() string { return bootstrap.UpgradeKubeletScript(v) })
					}, v.String())
				}
				return err
			})
			if err != nil {
				return pause(n, err)
			}
		}
		if err := o.setClusterVersion(p.ClusterID, v.String()); err != nil {
			return err
		}
		jc.Logf(nil, "✔ Cluster em %s", v)
	}
	return jc.SetResult(context.Background(), UpgradeResult{From: p.From, To: hops[len(hops)-1].String(), Hops: p.Hops})
}

func (o *Ops) setClusterVersion(id, version string) error {
	ctx := context.Background()
	cl, err := o.Store.GetCluster(ctx, id)
	if err != nil {
		return err
	}
	cl.KubernetesVersion = version
	return o.Store.UpdateCluster(ctx, cl)
}

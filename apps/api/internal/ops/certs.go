package ops

import (
	"context"
	"fmt"
	"time"

	"github.com/efbecker/k8sfront/apps/api/internal/bootstrap"
	"github.com/efbecker/k8sfront/apps/api/internal/jobs"
	"github.com/efbecker/k8sfront/apps/api/internal/store"
	"github.com/efbecker/k8sfront/apps/api/internal/upgrade"
)

// CertsParams: Action is check or renew.
type CertsParams struct {
	ClusterID string `json:"clusterId"`
	Action    string `json:"action"`
}

// NodeCerts are the kubeadm certificates of one control plane.
type NodeCerts struct {
	ServerID string         `json:"serverId"`
	Name     string         `json:"name"`
	Certs    []upgrade.Cert `json:"certs"`
}

// CertsResult is stored as the job result.
type CertsResult struct {
	Action    string      `json:"action"`
	Nodes     []NodeCerts `json:"nodes"`
	CheckedAt time.Time   `json:"checkedAt"`
}

// certs checks (or renews, one control plane at a time) the kubeadm
// certificates. After a renewal the new admin.conf replaces the stored one.
func (o *Ops) certs(ctx context.Context, jc *jobs.Context) error {
	var p CertsParams
	if err := jc.Params(&p); err != nil {
		return err
	}
	if p.Action != "check" && p.Action != "renew" {
		return fmt.Errorf("ação inválida: %s", p.Action)
	}
	b, cl, err := o.lifecycleRun(ctx, jc, p.ClusterID)
	if err != nil {
		return err
	}
	defer b.closeAll()
	res := CertsResult{Action: p.Action, Nodes: []NodeCerts{}}
	for _, n := range b.spec.ControlPlanes {
		id := n.ID
		var out string
		var err error
		if p.Action == "renew" {
			err = b.jc.StepOnce(ctx, "certs-renew", n.Name+": kubeadm certs renew all + restart do control plane", &id, func(ctx context.Context, st *store.JobStep) error {
				r, err := b.on(ctx, st, n, n, bootstrap.CertsRenewScript())
				if r != nil {
					out = r.Stdout
				}
				return err
			})
			if err != nil {
				return pause(n, err)
			}
		}
		if out == "" {
			err = jc.Step(ctx, "certs-check", n.Name+": kubeadm certs check-expiration", &id, func(ctx context.Context, st *store.JobStep) error {
				r, err := b.on(ctx, st, n, n, bootstrap.CertsCheckScript())
				if r != nil {
					out = r.Stdout
				}
				return err
			})
			if err != nil {
				return err
			}
		}
		res.Nodes = append(res.Nodes, NodeCerts{ServerID: n.ID, Name: n.Name, Certs: upgrade.ParseCheckExpiration(out)})
	}
	if p.Action == "renew" {
		primary := b.primary()
		pid := primary.ID
		if err := jc.Step(ctx, "kubeconfig", primary.Name+": atualizar admin.conf no cofre", &pid, func(ctx context.Context, _ *store.JobStep) error {
			_, err := b.storeKubeconfig(ctx, cl.ID, primary)
			return err
		}); err != nil {
			return err
		}
	}
	res.CheckedAt = time.Now().UTC()
	return jc.SetResult(context.Background(), res)
}

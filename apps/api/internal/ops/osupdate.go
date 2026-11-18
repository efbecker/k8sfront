package ops

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/efbecker/k8sfront/apps/api/internal/bootstrap"
	"github.com/efbecker/k8sfront/apps/api/internal/jobs"
	"github.com/efbecker/k8sfront/apps/api/internal/ospatch"
	"github.com/efbecker/k8sfront/apps/api/internal/store"
)

// ClusterParams target a whole cluster.
type ClusterParams struct {
	ClusterID string `json:"clusterId"`
}

// OSCheckResult is the APT state of every node.
type OSCheckResult struct {
	Nodes     []ospatch.NodeReport `json:"nodes"`
	CheckedAt time.Time            `json:"checkedAt"`
}

// Reboot policies of cluster.os-update.
const (
	RebootNever      = "never"
	RebootIfRequired = "if-required"
	RebootAlways     = "always"
)

// OSUpdateParams: ServerIDs empty means every node.
type OSUpdateParams struct {
	ClusterID string   `json:"clusterId"`
	ServerIDs []string `json:"serverIds,omitempty"`
	Reboot    string   `json:"reboot"`
}

// RollingRebootParams: ServerIDs empty means every node.
type RollingRebootParams struct {
	ClusterID string   `json:"clusterId"`
	ServerIDs []string `json:"serverIds,omitempty"`
}

// osCheck refreshes APT on every node in parallel and reports upgradable
// packages (security ones flagged), holds and pending reboots.
func (o *Ops) osCheck(ctx context.Context, jc *jobs.Context) error {
	var p ClusterParams
	if err := jc.Params(&p); err != nil {
		return err
	}
	b, _, err := o.lifecycleRun(ctx, jc, p.ClusterID)
	if err != nil {
		return err
	}
	defer b.closeAll()
	nodes := b.orderedNodes(nil)
	reports := make([]ospatch.NodeReport, len(nodes))
	var wg sync.WaitGroup
	for i, n := range nodes {
		wg.Add(1)
		go func() {
			defer wg.Done()
			id := n.ID
			var rep ospatch.NodeReport
			err := jc.Step(ctx, "os-check", n.Name+": apt-get update e pacotes atualizáveis", &id, func(ctx context.Context, st *store.JobStep) error {
				res, err := b.on(ctx, st, n, n, bootstrap.OSCheckScript())
				if err != nil {
					return err
				}
				rep = ospatch.Parse(res.Stdout)
				jc.Sink(st, &id)(store.StreamSystem, fmt.Sprintf("  %d atualizável(is), %d de segurança, reboot pendente: %v", len(rep.Packages), rep.Security, rep.RebootRequired))
				return nil
			})
			if err != nil {
				rep = ospatch.NodeReport{Packages: []ospatch.Package{}, Held: []string{}, Error: err.Error()}
			}
			rep.ServerID, rep.Name = n.ID, n.Name
			reports[i] = rep
		}()
	}
	wg.Wait()
	if err := jc.SetResult(context.Background(), OSCheckResult{Nodes: reports, CheckedAt: time.Now().UTC()}); err != nil {
		return err
	}
	var failed []string
	for _, r := range reports {
		if r.Error != "" {
			failed = append(failed, r.Name)
		}
	}
	if len(failed) > 0 {
		return fmt.Errorf("falha em %d nó(s): %s", len(failed), strings.Join(failed, ", "))
	}
	return nil
}

const readRebootRequired = "if [ -f /var/run/reboot-required ]; then echo yes; fi"

// osUpdate applies APT updates one node at a time (holds preserved) and,
// per the reboot policy, reboots with cordon → drain → reboot → Ready → uncordon.
func (o *Ops) osUpdate(ctx context.Context, jc *jobs.Context) error {
	var p OSUpdateParams
	if err := jc.Params(&p); err != nil {
		return err
	}
	if p.Reboot == "" {
		p.Reboot = RebootIfRequired
	}
	if p.Reboot != RebootNever && p.Reboot != RebootIfRequired && p.Reboot != RebootAlways {
		return fmt.Errorf("política de reboot inválida: %s", p.Reboot)
	}
	return o.rolling(ctx, jc, p.ClusterID, p.ServerIDs, true, p.Reboot)
}

// rollingReboot reboots nodes one at a time with drain and Ready checks.
func (o *Ops) rollingReboot(ctx context.Context, jc *jobs.Context) error {
	var p RollingRebootParams
	if err := jc.Params(&p); err != nil {
		return err
	}
	return o.rolling(ctx, jc, p.ClusterID, p.ServerIDs, false, RebootAlways)
}

func (o *Ops) rolling(ctx context.Context, jc *jobs.Context, clusterID string, ids []string, upgrade bool, reboot string) error {
	b, _, err := o.lifecycleRun(ctx, jc, clusterID)
	if err != nil {
		return err
	}
	defer b.closeAll()
	nodes := b.orderedNodes(ids)
	if len(nodes) == 0 {
		return fmt.Errorf("nenhum nó selecionado")
	}
	single := len(allNodes(b.spec)) == 1
	var names map[string]string
	if reboot != RebootNever {
		if names, err = b.resolveNames(ctx, nodes); err != nil {
			return err
		}
	}
	rebooted := 0
	for _, n := range nodes {
		didReboot := false
		err := b.retryNode(ctx, n, func(ctx context.Context, final bool) error {
			if upgrade {
				if err := b.nodeStep(ctx, n, "os-upgrade", "apt-get upgrade (holds preservados)", bootstrap.OSUpgradeScript); err != nil {
					return err
				}
			}
			do := reboot == RebootAlways
			if reboot == RebootIfRequired {
				out, err := b.quiet(ctx, n, readRebootRequired)
				if err != nil {
					return err
				}
				do = strings.TrimSpace(out) == "yes"
				if !do {
					jc.Logf(&n.ID, "  %s: nenhum reboot pendente", n.Name)
				}
			}
			if !do {
				return nil
			}
			didReboot = true
			return b.maintain(ctx, n, names[n.ID], "", single, final, func(ctx context.Context) error {
				return b.rebootAndWait(ctx, n, "reboot")
			}, "")
		})
		if err != nil {
			return pause(n, err)
		}
		if didReboot {
			rebooted++
		}
	}
	jc.Logf(nil, "✔ %d nó(s) processado(s), %d reiniciado(s)", len(nodes), rebooted)
	return nil
}

package ops

import (
	"context"
	"fmt"

	"github.com/efbecker/k8sfront/apps/api/internal/bootstrap"
	"github.com/efbecker/k8sfront/apps/api/internal/jobs"
	"github.com/efbecker/k8sfront/apps/api/internal/store"
)

// HelmParams describe a Helm operation. Values are never in the params: they
// live encrypted in the vault (ValuesSecretID) until the job succeeds.
type HelmParams struct {
	ClusterID      string `json:"clusterId"`
	Action         string `json:"action"`
	Release        string `json:"release"`
	Namespace      string `json:"namespace"`
	Chart          string `json:"chart,omitempty"`
	Repo           string `json:"repo,omitempty"`
	Version        string `json:"version,omitempty"`
	ValuesSecretID string `json:"valuesSecretId,omitempty"`
}

// helm runs the Helm CLI on the primary control plane.
func (o *Ops) helm(ctx context.Context, jc *jobs.Context) error {
	var p HelmParams
	if err := jc.Params(&p); err != nil {
		return err
	}
	b, _, err := o.lifecycleRun(ctx, jc, p.ClusterID)
	if err != nil {
		return err
	}
	defer b.closeAll()
	req := bootstrap.HelmRequest{Action: bootstrap.HelmAction(p.Action), Release: p.Release, Namespace: p.Namespace,
		Chart: p.Chart, Repo: p.Repo, Version: p.Version}
	if req.Action != bootstrap.HelmInstall && req.Action != bootstrap.HelmUninstall {
		return fmt.Errorf("ação inválida: %s", p.Action)
	}
	if p.ValuesSecretID != "" {
		if o.Secrets == nil {
			return fmt.Errorf("secrets manager not configured")
		}
		v, err := o.Secrets.Get(ctx, p.ValuesSecretID)
		if err != nil {
			return fmt.Errorf("values do release: %w", err)
		}
		req.Values = string(v)
		for i := range v {
			v[i] = 0
		}
	}
	primary := b.primary()
	pid := primary.ID
	name := fmt.Sprintf("helm upgrade --install %s (%s) -n %s", p.Release, p.Chart, p.Namespace)
	if req.Action == bootstrap.HelmUninstall {
		name = fmt.Sprintf("helm uninstall %s -n %s", p.Release, p.Namespace)
	}
	if err := jc.Step(ctx, "helm", name, &pid, func(ctx context.Context, st *store.JobStep) error {
		_, err := b.on(ctx, st, primary, primary, bootstrap.HelmScript(req))
		return err
	}); err != nil {
		return err
	}
	if p.ValuesSecretID != "" {
		_ = o.Secrets.Delete(context.Background(), p.ValuesSecretID)
	}
	return nil
}

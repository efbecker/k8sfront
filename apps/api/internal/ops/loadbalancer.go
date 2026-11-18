package ops

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/efbecker/k8sfront/apps/api/internal/bootstrap"
	"github.com/efbecker/k8sfront/apps/api/internal/jobs"
	"github.com/efbecker/k8sfront/apps/api/internal/store"
)

// LoadBalancerParams installs (or reconfigures) the bare-metal load balancer
// of a provisioned cluster.
type LoadBalancerParams struct {
	ClusterID    string                 `json:"clusterId"`
	LoadBalancer bootstrap.LoadBalancer `json:"loadBalancer"`
}

// loadBalancer installs MetalLB with the requested pool on the primary
// control plane and records the pool in the cluster options once it is
// applied. Re-running it only updates the pool (the manifest apply is
// idempotent).
func (o *Ops) loadBalancer(ctx context.Context, jc *jobs.Context) error {
	var p LoadBalancerParams
	if err := jc.Params(&p); err != nil {
		return err
	}
	b, cl, err := o.lifecycleRun(ctx, jc, p.ClusterID)
	if err != nil {
		return err
	}
	defer b.closeAll()
	lb := p.LoadBalancer
	b.spec.Options.LoadBalancer = &lb
	if err := b.spec.Validate(); err != nil {
		return err
	}
	primary := b.primary()
	pid := primary.ID
	lctx, cancel := context.WithTimeout(ctx, 20*time.Minute)
	err = jc.Step(lctx, "loadbalancer", primary.Name+": MetalLB "+bootstrap.DefaultMetalLBVersion+" + faixa de IPs", &pid, func(ctx context.Context, st *store.JobStep) error {
		_, err := b.on(ctx, st, primary, primary, b.spec.LoadBalancerScript())
		return err
	})
	cancel()
	if err != nil {
		if errors.Is(err, context.DeadlineExceeded) && ctx.Err() == nil {
			return fmt.Errorf("MetalLB: tempo esgotado após 20m: %w", err)
		}
		return err
	}
	// Re-read: other options may have changed while the job ran.
	cur, err := o.Store.GetCluster(context.Background(), cl.ID)
	if err != nil {
		return err
	}
	opts, err := bootstrap.ParseOptions(cur.Options)
	if err != nil {
		return err
	}
	opts.LoadBalancer = &lb
	raw, _ := json.Marshal(opts)
	cur.Options = raw
	if err := o.Store.UpdateCluster(context.Background(), cur); err != nil {
		return err
	}
	return jc.SetResult(context.Background(), map[string]any{"provider": lb.Provider, "addresses": lb.Addresses})
}

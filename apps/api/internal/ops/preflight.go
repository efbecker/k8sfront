// Package ops contains the job handlers that act on servers and clusters.
package ops

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sync"
	"time"

	"github.com/efbecker/k8sfront/apps/api/internal/jobs"
	"github.com/efbecker/k8sfront/apps/api/internal/preflight"
	"github.com/efbecker/k8sfront/apps/api/internal/redact"
	"github.com/efbecker/k8sfront/apps/api/internal/remote"
	"github.com/efbecker/k8sfront/apps/api/internal/secrets"
	"github.com/efbecker/k8sfront/apps/api/internal/sshx"
	"github.com/efbecker/k8sfront/apps/api/internal/store"
)

// KindPreflight tests SSH, sudo, Debian 13+ and node prerequisites for one or
// more servers, in parallel.
const KindPreflight = "servers.preflight"

// PreflightParams are the job parameters.
type PreflightParams struct {
	ServerIDs []string `json:"serverIds"`
}

// PreflightResult maps server ID to its report.
type PreflightResult struct {
	Status  preflight.Status             `json:"status"`
	Reports map[string]*preflight.Report `json:"reports"`
}

// Ops bundles handler dependencies.
type Ops struct {
	Store     store.Store
	Connector *remote.Connector
	Secrets   *secrets.Manager
	// Open overrides how node sessions are opened (tests); defaults to Connector.Open.
	Open func(ctx context.Context, s *store.Server, hooks remote.Hooks) (Runner, error)
	// ReadyTimeout bounds the final wait for all nodes to be Ready (default 15m).
	ReadyTimeout time.Duration
	// PollInterval is the Ready polling interval (default 10s).
	PollInterval time.Duration
}

// Runner is an open, validated session on a node.
type Runner interface {
	Run(ctx context.Context, script string, opts sshx.RunOptions, sink sshx.Sink) (*sshx.Result, error)
	Redactor() *redact.Redactor
	Close() error
}

func (o *Ops) open(ctx context.Context, s *store.Server, hooks remote.Hooks) (Runner, error) {
	if o.Open != nil {
		return o.Open(ctx, s, hooks)
	}
	sess, err := o.Connector.Open(ctx, s, hooks)
	if err != nil {
		return nil, err
	}
	return sess, nil
}

// Register installs every handler on the engine.
func (o *Ops) Register(e *jobs.Engine) {
	e.Register(KindPreflight, o.preflight)
	e.Register(KindBootstrap, o.bootstrap)
}

func (o *Ops) preflight(ctx context.Context, jc *jobs.Context) error {
	var p PreflightParams
	if err := jc.Params(&p); err != nil {
		return err
	}
	if len(p.ServerIDs) == 0 {
		return errors.New("no servers to check")
	}
	res := &PreflightResult{Reports: map[string]*preflight.Report{}}
	var mu sync.Mutex
	var wg sync.WaitGroup
	for _, id := range p.ServerIDs {
		srv, err := o.Store.GetServer(ctx, id)
		if err != nil {
			return fmt.Errorf("server %s: %w", id, err)
		}
		wg.Add(1)
		go func() {
			defer wg.Done()
			rep := o.checkServer(ctx, jc, srv)
			mu.Lock()
			res.Reports[srv.ID] = rep
			mu.Unlock()
		}()
	}
	wg.Wait()
	failed := 0
	for _, r := range res.Reports {
		res.Status = worstStatus(res.Status, r.Status)
		if r.Status == preflight.Fail {
			failed++
		}
	}
	if err := jc.SetResult(context.Background(), res); err != nil {
		return err
	}
	if ctx.Err() != nil {
		return ctx.Err()
	}
	if failed > 0 {
		return fmt.Errorf("%d of %d server(s) failed the prerequisites", failed, len(res.Reports))
	}
	return nil
}

func worstStatus(a, b preflight.Status) preflight.Status {
	r := &preflight.Report{Status: a}
	r.Add(preflight.Check{Status: b})
	return r.Status
}

// checkServer never returns an error: failures become checks in the report.
func (o *Ops) checkServer(ctx context.Context, jc *jobs.Context, srv *store.Server) *preflight.Report {
	sid := srv.ID
	rep := &preflight.Report{Role: srv.Role}
	label := fmt.Sprintf("%s (%s)", srv.Name, srv.Host)

	stageNames := map[remote.Stage]string{
		remote.StageConnect: "Conexão SSH e host key",
		remote.StageOS:      "Sistema operacional Debian 13+",
		remote.StageSudo:    "Elevação de privilégio (sudo)",
	}
	// Each Open stage becomes a job step and a report check.
	var cur *store.JobStep
	hooks := remote.Hooks{
		Before: func(stage remote.Stage) {
			cur, _ = jc.BeginStep(ctx, "preflight."+string(stage), label+": "+stageNames[stage], &sid)
		},
		After: func(stage remote.Stage, detail string, err error) {
			c := preflight.Check{ID: string(stage), Name: stageNames[stage], Status: preflight.Pass, Detail: detail}
			if err != nil {
				c.Status, c.Detail = preflight.Fail, err.Error()
			} else if detail != "" {
				jc.Logf(&sid, "  %s", detail)
			}
			rep.Add(c)
			if cur != nil {
				_ = jc.EndStep(cur, err)
				cur = nil
			}
		},
	}
	sess, err := o.Connector.Open(ctx, srv, hooks)
	defer o.saveReport(srv.ID, rep)
	if err != nil {
		var oe *remote.OpenError
		if !errors.As(err, &oe) {
			rep.Add(preflight.Check{ID: "connect", Name: stageNames[remote.StageConnect], Status: preflight.Fail, Detail: err.Error()})
		}
		jc.Logf(&sid, "%s: aborting checks: %v", label, err)
		return rep
	}
	defer sess.Close()
	rep.OS, rep.Sudo = sess.OS, sess.Sudo

	_ = jc.Step(ctx, "preflight.resources", label+": recursos, relógio e portas", &sid, func(ctx context.Context, step *store.JobStep) error {
		facts, err := preflight.Gather(ctx, sess.Client)
		if err != nil {
			rep.Add(preflight.Check{ID: "facts", Name: "Coleta de informações do host", Status: preflight.Fail, Detail: err.Error()})
			return err
		}
		rep.Facts = facts
		srv.Kernel = facts.Kernel
		var failed []string
		for _, c := range preflight.Evaluate(facts, srv.Role) {
			rep.Add(c)
			icon := map[preflight.Status]string{preflight.Pass: "✔", preflight.Warn: "⚠", preflight.Fail: "✖"}[c.Status]
			jc.Sink(step, &sid)("system", fmt.Sprintf("  %s %s: %s", icon, c.Name, c.Detail))
			if c.Status == preflight.Fail {
				failed = append(failed, c.Name)
			}
		}
		if len(failed) > 0 {
			return fmt.Errorf("requisitos não atendidos: %v", failed)
		}
		return nil
	})
	return rep
}

func (o *Ops) saveReport(serverID string, rep *preflight.Report) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	rep.CheckedAt = time.Now()
	srv, err := o.Store.GetServer(ctx, serverID)
	if err != nil {
		return
	}
	raw, _ := json.Marshal(rep)
	srv.LastCheckAt, srv.LastCheckStatus, srv.LastCheckReport = &rep.CheckedAt, string(rep.Status), raw
	if rep.Facts != nil {
		srv.Kernel = rep.Facts.Kernel
	}
	_ = o.Store.UpdateServer(ctx, srv)
}

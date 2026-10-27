package jobs

import (
	"context"
	"encoding/json"
	"fmt"
	"sync"
	"time"

	"github.com/efbecker/k8sfront/apps/api/internal/redact"
	"github.com/efbecker/k8sfront/apps/api/internal/store"
)

// Context is the handler's view of its job.
type Context struct {
	Job    *store.Job
	engine *Engine
	mu     sync.Mutex
}

func nowPtr() *time.Time {
	t := time.Now()
	return &t
}

// Params decodes the job parameters.
func (c *Context) Params(v any) error { return json.Unmarshal(c.Job.Params, v) }

// SetResult stores the job's structured result.
func (c *Context) SetResult(ctx context.Context, v any) error {
	raw, err := json.Marshal(v)
	if err != nil {
		return err
	}
	return c.engine.st.SetJobResult(ctx, c.Job.ID, raw)
}

// Logf writes a system line (redacted with the generic patterns).
func (c *Context) Logf(serverID *string, format string, args ...any) {
	c.engine.hub.Append(store.LogLine{JobID: c.Job.ID, Stream: store.StreamSystem, ServerID: serverID,
		Line: redact.New().String(fmt.Sprintf(format, args...))})
}

// Sink returns an output sink that tags lines with a step and server. Lines
// handed to it must already be redacted (sshx sinks are).
func (c *Context) Sink(step *store.JobStep, serverID *string) func(stream store.LogStream, line string) {
	var stepID *int64
	if step != nil {
		id := step.ID
		stepID = &id
	}
	return func(stream store.LogStream, line string) {
		c.engine.hub.Append(store.LogLine{JobID: c.Job.ID, Stream: stream, StepID: stepID, ServerID: serverID, Line: line})
	}
}

// StepFunc is the body of a step.
type StepFunc func(ctx context.Context, step *store.JobStep) error

// Step records and runs a unit of progress, always executing it.
func (c *Context) Step(ctx context.Context, key, name string, serverID *string, fn StepFunc) error {
	return c.step(ctx, key, name, serverID, false, fn)
}

// StepOnce is like Step but skips the body when a previous attempt of this job
// already completed the same key for the same server (resumable pipelines).
func (c *Context) StepOnce(ctx context.Context, key, name string, serverID *string, fn StepFunc) error {
	return c.step(ctx, key, name, serverID, true, fn)
}

func sameServer(a, b *string) bool {
	if a == nil || b == nil {
		return a == nil && b == nil
	}
	return *a == *b
}

func (c *Context) step(ctx context.Context, key, name string, serverID *string, once bool, fn StepFunc) error {
	st := c.engine.st
	if once {
		prev, err := st.ListSteps(ctx, c.Job.ID)
		if err != nil {
			return err
		}
		for _, p := range prev {
			if p.Key == key && sameServer(p.ServerID, serverID) && p.Status == store.StepSucceeded {
				skipped := &store.JobStep{JobID: c.Job.ID, Key: key, Name: name, ServerID: serverID, Status: store.StepSkipped,
					StartedAt: nowPtr(), FinishedAt: nowPtr()}
				if err := st.CreateStep(ctx, skipped); err != nil {
					return err
				}
				c.Logf(serverID, "⏭ %s: already completed in a previous attempt, skipping", name)
				c.engine.publish(ctx, c.Job.ID)
				return nil
			}
		}
	}
	step, err := c.BeginStep(ctx, key, name, serverID)
	if err != nil {
		return err
	}
	err = fn(ctx, step)
	return c.EndStep(step, err)
}

// BeginStep records a running step. Pair every call with EndStep.
func (c *Context) BeginStep(ctx context.Context, key, name string, serverID *string) (*store.JobStep, error) {
	step := &store.JobStep{JobID: c.Job.ID, Key: key, Name: name, ServerID: serverID, Status: store.StepRunning, StartedAt: nowPtr()}
	if err := c.engine.st.CreateStep(ctx, step); err != nil {
		return nil, err
	}
	c.engine.hub.Append(store.LogLine{JobID: c.Job.ID, Stream: store.StreamSystem, StepID: &step.ID, ServerID: serverID, Line: "▶ " + name})
	c.engine.publish(ctx, c.Job.ID)
	return step, nil
}

// EndStep marks a step finished with err's outcome and returns err.
func (c *Context) EndStep(step *store.JobStep, err error) error {
	bctx := context.Background()
	step.FinishedAt = nowPtr()
	if err != nil {
		step.Status, step.Error = store.StepFailed, redact.New().String(err.Error())
		c.engine.hub.Append(store.LogLine{JobID: c.Job.ID, Stream: store.StreamSystem, StepID: &step.ID, ServerID: step.ServerID, Line: "✖ " + step.Name + ": " + step.Error})
	} else {
		step.Status = store.StepSucceeded
		c.engine.hub.Append(store.LogLine{JobID: c.Job.ID, Stream: store.StreamSystem, StepID: &step.ID, ServerID: step.ServerID, Line: "✔ " + step.Name})
	}
	if uerr := c.engine.st.UpdateStep(bctx, step); uerr != nil && err == nil {
		err = uerr
	}
	c.engine.publish(bctx, c.Job.ID)
	return err
}

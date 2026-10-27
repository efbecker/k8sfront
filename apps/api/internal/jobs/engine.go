// Package jobs runs long operations in the background, independent of any
// HTTP request. State lives in the store, so a browser can disconnect and
// reconnect at will, and a platform restart pauses (never silently drops)
// interrupted work.
package jobs

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"runtime/debug"
	"sync"

	"github.com/efbecker/k8sfront/apps/api/internal/store"
	"github.com/google/uuid"
)

// Handler executes one job kind. It must be idempotent: a retried job runs the
// handler again from the start (use Context.StepOnce to skip finished steps).
type Handler func(ctx context.Context, jc *Context) error

// PauseError makes the job end in "paused" (circuit breaker) instead of
// "failed". Paused jobs can be retried or aborted by the operator.
type PauseError struct{ Err error }

func (e *PauseError) Error() string { return e.Err.Error() }
func (e *PauseError) Unwrap() error { return e.Err }

// Pause wraps err so the engine pauses the job.
func Pause(err error) error { return &PauseError{Err: err} }

// Engine dispatches jobs to handlers on a worker pool.
type Engine struct {
	st       store.Store
	hub      *Hub
	log      *slog.Logger
	handlers map[string]Handler
	queue    chan string
	workers  int

	mu       sync.Mutex
	running  map[string]*runState
	locks    map[string]*sync.Mutex // per-target serialization
	stopping bool
	baseCtx  context.Context
	stop     context.CancelFunc
	wg       sync.WaitGroup
}

type runState struct {
	cancel    context.CancelFunc
	cancelled bool // operator asked to cancel
}

// New creates an engine with n workers.
func New(st store.Store, hub *Hub, logger *slog.Logger, workers int) *Engine {
	if workers <= 0 {
		workers = 4
	}
	return &Engine{
		st: st, hub: hub, log: logger, workers: workers,
		handlers: map[string]Handler{},
		queue:    make(chan string, 1024),
		running:  map[string]*runState{},
		locks:    map[string]*sync.Mutex{},
	}
}

// Hub exposes the log hub.
func (e *Engine) Hub() *Hub { return e.hub }

// Register binds a handler to a job kind. Call before Start.
func (e *Engine) Register(kind string, h Handler) { e.handlers[kind] = h }

// Start recovers persisted jobs and launches workers.
func (e *Engine) Start(ctx context.Context) error {
	e.baseCtx, e.stop = context.WithCancel(context.Background())
	// Jobs that were running when the process died are paused for the operator.
	interrupted, err := e.st.ListJobs(ctx, store.JobFilter{Status: []store.JobStatus{store.JobRunning}, Limit: 500})
	if err != nil {
		return err
	}
	for _, j := range interrupted {
		e.markRunningStepsFailed(ctx, j.ID, "interrupted by platform restart")
		if err := e.st.TransitionJob(ctx, j.ID, []store.JobStatus{store.JobRunning}, store.JobPaused,
			"interrupted by platform restart; use Retry to resume"); err != nil {
			e.log.Error("pause interrupted job", "job", j.ID, "err", err)
		}
		e.hub.Append(store.LogLine{JobID: j.ID, Stream: store.StreamSystem, Line: "⏸ platform restarted while this job was running; job paused, retry to resume"})
	}
	for i := 0; i < e.workers; i++ {
		e.wg.Add(1)
		go e.worker()
	}
	pending, err := e.st.ListJobs(ctx, store.JobFilter{Status: []store.JobStatus{store.JobPending}, Limit: 500})
	if err != nil {
		return err
	}
	for i := len(pending) - 1; i >= 0; i-- { // oldest first
		e.enqueue(pending[i].ID)
	}
	return nil
}

// Shutdown stops workers; running jobs are paused so they can be retried.
func (e *Engine) Shutdown(ctx context.Context) {
	e.mu.Lock()
	e.stopping = true
	e.mu.Unlock()
	if e.stop != nil {
		e.stop()
	}
	done := make(chan struct{})
	go func() { e.wg.Wait(); close(done) }()
	select {
	case <-done:
	case <-ctx.Done():
	}
	e.hub.Close()
}

func (e *Engine) enqueue(id string) {
	select {
	case e.queue <- id:
	default:
		go func() { e.queue <- id }()
	}
}

// Submit persists a new pending job and queues it.
func (e *Engine) Submit(ctx context.Context, kind, targetType, targetID string, params any, actor string) (*store.Job, error) {
	if _, ok := e.handlers[kind]; !ok {
		return nil, fmt.Errorf("unknown job kind %q", kind)
	}
	raw, err := json.Marshal(params)
	if err != nil {
		return nil, err
	}
	j := &store.Job{ID: uuid.NewString(), Kind: kind, Status: store.JobPending, TargetType: targetType, TargetID: targetID,
		Params: raw, CreatedBy: actor}
	if err := e.st.CreateJob(ctx, j); err != nil {
		return nil, err
	}
	e.hub.Append(store.LogLine{JobID: j.ID, Stream: store.StreamSystem, Line: fmt.Sprintf("job %s (%s) queued by %s", j.ID, kind, actor)})
	e.enqueue(j.ID)
	return j, nil
}

// Cancel aborts a pending, paused, failed or running job.
func (e *Engine) Cancel(ctx context.Context, id string) error {
	e.mu.Lock()
	if rs, ok := e.running[id]; ok {
		rs.cancelled = true
		rs.cancel()
		e.mu.Unlock()
		return nil
	}
	e.mu.Unlock()
	err := e.st.TransitionJob(ctx, id, []store.JobStatus{store.JobPending, store.JobPaused, store.JobFailed}, store.JobCancelled, "cancelled by operator")
	if err == nil {
		e.hub.Append(store.LogLine{JobID: id, Stream: store.StreamSystem, Line: "✖ job aborted by operator"})
		e.publish(ctx, id)
	}
	return err
}

// Retry re-queues a failed, paused or cancelled job.
func (e *Engine) Retry(ctx context.Context, id string) error {
	err := e.st.TransitionJob(ctx, id, []store.JobStatus{store.JobFailed, store.JobPaused, store.JobCancelled}, store.JobPending, "")
	if err != nil {
		return err
	}
	e.hub.Append(store.LogLine{JobID: id, Stream: store.StreamSystem, Line: "↻ retry requested; job queued"})
	e.publish(ctx, id)
	e.enqueue(id)
	return nil
}

func (e *Engine) worker() {
	defer e.wg.Done()
	for {
		select {
		case <-e.baseCtx.Done():
			return
		case id := <-e.queue:
			e.execute(id)
		}
	}
}

func (e *Engine) targetLock(j *store.Job) *sync.Mutex {
	if j.TargetID == "" {
		return nil
	}
	key := j.TargetType + "/" + j.TargetID
	e.mu.Lock()
	defer e.mu.Unlock()
	l, ok := e.locks[key]
	if !ok {
		l = &sync.Mutex{}
		e.locks[key] = l
	}
	return l
}

func (e *Engine) execute(id string) {
	ctx := e.baseCtx
	j, err := e.st.GetJob(ctx, id)
	if err != nil {
		e.log.Error("load job", "job", id, "err", err)
		return
	}
	if j.Status != store.JobPending {
		return // cancelled or already picked up
	}
	h, ok := e.handlers[j.Kind]
	if !ok {
		_ = e.st.TransitionJob(ctx, id, []store.JobStatus{store.JobPending}, store.JobFailed, "no handler for job kind "+j.Kind)
		return
	}
	if l := e.targetLock(j); l != nil {
		l.Lock()
		defer l.Unlock()
	}
	jctx, cancel := context.WithCancel(ctx)
	defer cancel()
	rs := &runState{cancel: cancel}
	e.mu.Lock()
	if e.stopping {
		e.mu.Unlock()
		return
	}
	e.running[id] = rs
	e.mu.Unlock()
	defer func() {
		e.mu.Lock()
		delete(e.running, id)
		e.mu.Unlock()
	}()

	if err := e.st.TransitionJob(ctx, id, []store.JobStatus{store.JobPending}, store.JobRunning, ""); err != nil {
		return
	}
	j, _ = e.st.GetJob(ctx, id)
	e.hub.Append(store.LogLine{JobID: id, Stream: store.StreamSystem, Line: fmt.Sprintf("▶ job started (attempt %d)", j.Attempts)})
	e.publish(ctx, id)

	jc := &Context{Job: j, engine: e}
	runErr := func() (err error) {
		defer func() {
			if r := recover(); r != nil {
				e.log.Error("job handler panic", "job", id, "panic", r, "stack", string(debug.Stack()))
				err = fmt.Errorf("internal error: %v", r)
			}
		}()
		return h(jctx, jc)
	}()

	// Use a fresh context for bookkeeping: jctx may be cancelled.
	bctx := context.Background()
	e.mu.Lock()
	cancelled, stopping := rs.cancelled, e.stopping
	e.mu.Unlock()
	var to store.JobStatus
	msg := ""
	var pe *PauseError
	switch {
	case cancelled:
		to, msg = store.JobCancelled, "cancelled by operator"
		e.hub.Append(store.LogLine{JobID: id, Stream: store.StreamSystem, Line: "✖ job cancelled by operator"})
	case stopping && runErr != nil:
		to, msg = store.JobPaused, "platform shutting down; retry to resume"
		e.hub.Append(store.LogLine{JobID: id, Stream: store.StreamSystem, Line: "⏸ platform shutting down; job paused"})
	case runErr == nil:
		to = store.JobSucceeded
		e.hub.Append(store.LogLine{JobID: id, Stream: store.StreamSystem, Line: "✔ job succeeded"})
	case errors.As(runErr, &pe):
		to, msg = store.JobPaused, runErr.Error()
		e.hub.Append(store.LogLine{JobID: id, Stream: store.StreamSystem, Line: "⏸ circuit breaker: job paused: " + msg})
	default:
		to, msg = store.JobFailed, runErr.Error()
		e.hub.Append(store.LogLine{JobID: id, Stream: store.StreamSystem, Line: "✖ job failed: " + msg})
	}
	if to != store.JobSucceeded {
		e.markRunningStepsFailed(bctx, id, msg)
	}
	if err := e.st.TransitionJob(bctx, id, []store.JobStatus{store.JobRunning}, to, msg); err != nil {
		e.log.Error("finish job", "job", id, "err", err)
	}
	e.publish(bctx, id)
}

func (e *Engine) markRunningStepsFailed(ctx context.Context, jobID, msg string) {
	steps, err := e.st.ListSteps(ctx, jobID)
	if err != nil {
		return
	}
	for i := range steps {
		if steps[i].Status == store.StepRunning || steps[i].Status == store.StepPending {
			steps[i].Status = store.StepFailed
			steps[i].Error = msg
			now := nowPtr()
			steps[i].FinishedAt = now
			_ = e.st.UpdateStep(ctx, &steps[i])
		}
	}
}

func (e *Engine) publish(ctx context.Context, id string) {
	j, err := e.st.GetJob(ctx, id)
	if err != nil {
		return
	}
	e.hub.PublishJob(j)
}

package jobs

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"path/filepath"
	"testing"
	"time"

	"github.com/efbecker/k8sfront/apps/api/internal/store"
	"github.com/efbecker/k8sfront/apps/api/internal/store/sqlite"
)

func newEngine(t *testing.T) (*Engine, store.Store) {
	t.Helper()
	db, err := sqlite.Open(context.Background(), filepath.Join(t.TempDir(), "jobs.db"))
	if err != nil {
		t.Fatal(err)
	}
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	e := New(db, NewHub(db, logger), logger, 2)
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		e.Shutdown(ctx)
		db.Close()
	})
	return e, db
}

func waitStatus(t *testing.T, st store.Store, id string, want store.JobStatus) *store.Job {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		j, err := st.GetJob(context.Background(), id)
		if err == nil && j.Status == want {
			return j
		}
		time.Sleep(20 * time.Millisecond)
	}
	j, _ := st.GetJob(context.Background(), id)
	t.Fatalf("job %s: status %s, want %s", id, j.Status, want)
	return nil
}

func TestJobLifecycleWithResumableSteps(t *testing.T) {
	e, st := newEngine(t)
	runs := map[string]int{}
	fail := true
	e.Register("test", func(ctx context.Context, jc *Context) error {
		for _, k := range []string{"a", "b"} {
			err := jc.StepOnce(ctx, k, "step "+k, nil, func(ctx context.Context, step *store.JobStep) error {
				runs[k]++
				jc.Sink(step, nil)(store.StreamStdout, "output of "+k)
				if k == "b" && fail {
					return errors.New("boom")
				}
				return nil
			})
			if err != nil {
				return Pause(err)
			}
		}
		return nil
	})
	if err := e.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	j, err := e.Submit(context.Background(), "test", "", "", map[string]string{}, "tester")
	if err != nil {
		t.Fatal(err)
	}
	waitStatus(t, st, j.ID, store.JobPaused)

	fail = false
	if err := e.Retry(context.Background(), j.ID); err != nil {
		t.Fatal(err)
	}
	done := waitStatus(t, st, j.ID, store.JobSucceeded)
	if runs["a"] != 1 || runs["b"] != 2 {
		t.Fatalf("step a must not rerun after success; runs = %v", runs)
	}
	if done.Attempts != 2 {
		t.Fatalf("attempts = %d", done.Attempts)
	}
	e.Hub().Flush()
	logs, _ := st.ListLogs(context.Background(), j.ID, 0, 0)
	for i := range logs {
		if logs[i].Seq != int64(i+1) {
			t.Fatalf("log seq gap at %d: %d", i, logs[i].Seq)
		}
	}
}

func TestCancelRunningJob(t *testing.T) {
	e, st := newEngine(t)
	started := make(chan struct{})
	e.Register("block", func(ctx context.Context, jc *Context) error {
		close(started)
		<-ctx.Done()
		return ctx.Err()
	})
	_ = e.Start(context.Background())
	j, _ := e.Submit(context.Background(), "block", "", "", nil, "tester")
	<-started
	if err := e.Cancel(context.Background(), j.ID); err != nil {
		t.Fatal(err)
	}
	waitStatus(t, st, j.ID, store.JobCancelled)
}

func TestSubscribeGetsLiveLinesAfterBacklog(t *testing.T) {
	e, st := newEngine(t)
	ctx := context.Background()
	if err := st.CreateJob(ctx, &store.Job{ID: "x", Kind: "k", Status: store.JobRunning}); err != nil {
		t.Fatal(err)
	}
	e.Hub().Append(store.LogLine{JobID: "x", Stream: store.StreamStdout, Line: "one"})
	e.Hub().Flush()
	sub := e.Hub().Subscribe("x")
	defer e.Hub().Unsubscribe(sub)
	e.Hub().Append(store.LogLine{JobID: "x", Stream: store.StreamStdout, Line: "two"})
	select {
	case ev := <-sub.C:
		if ev.Log == nil || ev.Log.Seq != 2 || ev.Log.Line != "two" {
			t.Fatalf("unexpected event %+v", ev)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("no live event")
	}
}

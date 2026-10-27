package api

import (
	"context"
	"net/http"
	"strconv"
	"time"

	"github.com/coder/websocket"
	"github.com/coder/websocket/wsjson"
	"github.com/efbecker/k8sfront/apps/api/internal/jobs"
	"github.com/efbecker/k8sfront/apps/api/internal/store"
)

func (s *Server) listJobs(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	f := store.JobFilter{TargetID: q.Get("targetId")}
	for _, st := range q["status"] {
		f.Status = append(f.Status, store.JobStatus(st))
	}
	f.Limit, _ = strconv.Atoi(q.Get("limit"))
	list, err := s.Store.ListJobs(r.Context(), f)
	if err != nil {
		s.storeError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, list)
}

func (s *Server) getJob(w http.ResponseWriter, r *http.Request) {
	j, err := s.Store.GetJob(r.Context(), r.PathValue("id"))
	if err != nil {
		s.storeError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, j)
}

func (s *Server) getJobLogs(w http.ResponseWriter, r *http.Request) {
	after, _ := strconv.ParseInt(r.URL.Query().Get("after"), 10, 64)
	limit, _ := strconv.Atoi(r.URL.Query().Get("limit"))
	logs, err := s.Store.ListLogs(r.Context(), r.PathValue("id"), after, limit)
	if err != nil {
		s.storeError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, logs)
}

func (s *Server) cancelJob(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if err := s.Engine.Cancel(r.Context(), id); err != nil {
		s.storeError(w, err)
		return
	}
	s.audit(r, "job.cancel", "job", id, nil)
	w.WriteHeader(http.StatusAccepted)
}

func (s *Server) retryJob(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if err := s.Engine.Retry(r.Context(), id); err != nil {
		s.storeError(w, err)
		return
	}
	s.audit(r, "job.retry", "job", id, nil)
	w.WriteHeader(http.StatusAccepted)
}

// streamMessage is the wire format of /jobs/{id}/stream.
//
//	{"type":"job","job":{...}}       job snapshot (sent first and on every change)
//	{"type":"log","log":{seq,...}}   one redacted log line, strictly increasing seq
//	{"type":"ready"}                 backlog replay finished; live from here on
type streamMessage struct {
	Type string         `json:"type"`
	Job  *store.Job     `json:"job,omitempty"`
	Log  *store.LogLine `json:"log,omitempty"`
}

// streamJob replays logs after ?after=<seq> and then streams live events. A
// client that reconnects with its last seq gets every line exactly once.
func (s *Server) streamJob(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if _, err := s.Store.GetJob(r.Context(), id); err != nil {
		s.storeError(w, err)
		return
	}
	after, _ := strconv.ParseInt(r.URL.Query().Get("after"), 10, 64)
	conn, err := websocket.Accept(w, r, &websocket.AcceptOptions{OriginPatterns: s.AllowedOrigins})
	if err != nil {
		return
	}
	defer conn.CloseNow()
	ctx := conn.CloseRead(r.Context())

	// Subscribe before reading the backlog so nothing falls in between.
	sub := s.Engine.Hub().Subscribe(id)
	defer s.Engine.Hub().Unsubscribe(sub)

	send := func(m streamMessage) error {
		wctx, cancel := context.WithTimeout(ctx, 10*time.Second)
		defer cancel()
		return wsjson.Write(wctx, conn, m)
	}
	job, err := s.Store.GetJob(ctx, id)
	if err != nil {
		conn.Close(websocket.StatusInternalError, "job lookup failed")
		return
	}
	if err := send(streamMessage{Type: "job", Job: job}); err != nil {
		return
	}
	last := after
	for {
		batch, err := s.Store.ListLogs(ctx, id, last, 2000)
		if err != nil {
			conn.Close(websocket.StatusInternalError, "log read failed")
			return
		}
		for i := range batch {
			if err := send(streamMessage{Type: "log", Log: &batch[i]}); err != nil {
				return
			}
			last = batch[i].Seq
		}
		if len(batch) < 2000 {
			break
		}
	}
	if err := send(streamMessage{Type: "ready"}); err != nil {
		return
	}

	ping := time.NewTicker(25 * time.Second)
	defer ping.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ping.C:
			pctx, cancel := context.WithTimeout(ctx, 10*time.Second)
			err := conn.Ping(pctx)
			cancel()
			if err != nil {
				return
			}
		case ev, ok := <-sub.C:
			if !ok {
				// Too slow; ask the client to reconnect from its last seq.
				conn.Close(websocket.StatusTryAgainLater, "subscriber lagging, reconnect")
				return
			}
			switch ev.Type {
			case jobs.EventLog:
				if ev.Log.Seq <= last {
					continue
				}
				last = ev.Log.Seq
				if err := send(streamMessage{Type: "log", Log: ev.Log}); err != nil {
					return
				}
			case jobs.EventJob:
				if err := send(streamMessage{Type: "job", Job: ev.Job}); err != nil {
					return
				}
			}
		}
	}
}

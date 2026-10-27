package jobs

import (
	"context"
	"log/slog"
	"sync"
	"time"

	"github.com/efbecker/k8sfront/apps/api/internal/store"
)

// EventType distinguishes stream events.
type EventType string

const (
	EventLog EventType = "log"
	EventJob EventType = "job"
)

// Event is pushed to stream subscribers.
type Event struct {
	Type EventType      `json:"type"`
	Log  *store.LogLine `json:"log,omitempty"`
	Job  *store.Job     `json:"job,omitempty"`
}

// Subscriber receives events for one job. C is closed when the subscriber
// falls too far behind (it should reconnect from its last seq) or unsubscribes.
type Subscriber struct {
	C      chan Event
	jobID  string
	closed bool
}

// Hub persists log lines in batches and fans them out to subscribers.
//
// Ordering guarantee: a line is broadcast only after it is committed, so a
// client that subscribes first and then reads the backlog from the store
// (deduplicating by seq) never misses a line.
type Hub struct {
	st     store.Store
	log    *slog.Logger
	in     chan store.LogLine
	mu     sync.Mutex
	subs   map[string]map[*Subscriber]struct{}
	seqs   map[string]int64
	flushC chan chan struct{}
	done   chan struct{}
}

const (
	subscriberBuffer = 2048
	batchSize        = 256
	batchInterval    = 100 * time.Millisecond
)

// NewHub starts the background writer.
func NewHub(st store.Store, logger *slog.Logger) *Hub {
	h := &Hub{
		st: st, log: logger,
		in:     make(chan store.LogLine, 4096),
		subs:   map[string]map[*Subscriber]struct{}{},
		seqs:   map[string]int64{},
		flushC: make(chan chan struct{}),
		done:   make(chan struct{}),
	}
	go h.run()
	return h
}

// Append queues a redacted line. Seq and TS are assigned by the hub.
func (h *Hub) Append(l store.LogLine) {
	if l.TS.IsZero() {
		l.TS = time.Now()
	}
	select {
	case h.in <- l:
	case <-h.done:
	}
}

// Flush blocks until every queued line is persisted and broadcast.
func (h *Hub) Flush() {
	ack := make(chan struct{})
	select {
	case h.flushC <- ack:
		<-ack
	case <-h.done:
	}
}

// Close flushes and stops the writer.
func (h *Hub) Close() {
	h.Flush()
	close(h.done)
}

func (h *Hub) run() {
	t := time.NewTicker(batchInterval)
	defer t.Stop()
	var batch []store.LogLine
	flush := func() {
		if len(batch) == 0 {
			return
		}
		h.commit(batch)
		batch = batch[:0]
	}
	for {
		select {
		case l := <-h.in:
			batch = append(batch, l)
			if len(batch) >= batchSize {
				flush()
			}
		case <-t.C:
			flush()
		case ack := <-h.flushC:
			for drained := false; !drained; {
				select {
				case l := <-h.in:
					batch = append(batch, l)
				default:
					drained = true
				}
			}
			flush()
			close(ack)
		case <-h.done:
			return
		}
	}
}

func (h *Hub) commit(batch []store.LogLine) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	for i := range batch {
		jobID := batch[i].JobID
		seq, ok := h.seqs[jobID]
		if !ok {
			var err error
			if seq, err = h.st.MaxLogSeq(ctx, jobID); err != nil {
				h.log.Error("log seq lookup failed", "job", jobID, "err", err)
			}
		}
		seq++
		h.seqs[jobID] = seq
		batch[i].Seq = seq
	}
	if err := h.st.AppendLogs(ctx, batch); err != nil {
		h.log.Error("persist job logs failed", "err", err, "lines", len(batch))
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	for i := range batch {
		l := batch[i]
		for s := range h.subs[l.JobID] {
			h.sendLocked(s, Event{Type: EventLog, Log: &l})
		}
	}
}

// PublishJob broadcasts a job snapshot (status/steps changed).
func (h *Hub) PublishJob(j *store.Job) {
	// Make sure log lines emitted before the status change reach clients first.
	h.Flush()
	h.mu.Lock()
	defer h.mu.Unlock()
	for s := range h.subs[j.ID] {
		h.sendLocked(s, Event{Type: EventJob, Job: j})
	}
}

func (h *Hub) sendLocked(s *Subscriber, ev Event) {
	if s.closed {
		return
	}
	select {
	case s.C <- ev:
	default:
		// Too slow: drop it; the client reconnects from its last seq.
		s.closed = true
		close(s.C)
		delete(h.subs[s.jobID], s)
	}
}

// Subscribe registers for live events of a job. Call Unsubscribe when done.
func (h *Hub) Subscribe(jobID string) *Subscriber {
	s := &Subscriber{C: make(chan Event, subscriberBuffer), jobID: jobID}
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.subs[jobID] == nil {
		h.subs[jobID] = map[*Subscriber]struct{}{}
	}
	h.subs[jobID][s] = struct{}{}
	return s
}

// Unsubscribe removes a subscriber.
func (h *Hub) Unsubscribe(s *Subscriber) {
	h.mu.Lock()
	defer h.mu.Unlock()
	if s.closed {
		return
	}
	s.closed = true
	close(s.C)
	delete(h.subs[s.jobID], s)
	if len(h.subs[s.jobID]) == 0 {
		delete(h.subs, s.jobID)
	}
}

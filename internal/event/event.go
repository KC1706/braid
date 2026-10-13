// Package event delivers the merge engine's live event stream to the dashboard
// Orchestrator Durable Object. The engine exposes an Emit seam (merge.Options.Emit
// / models.Emitter) that is deliberately best-effort: instrumentation must never
// slow down or break an integration. HTTPEmitter is the production transport —
// ordered, non-blocking, seq-numbered POSTs to {worker}/runs/{id}/event — the Go
// counterpart of the hackathon TS BraidEmitter (GO_PRODUCTION_PLAN.md Phase 1.4).
package event

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/KC1706/braid/pkg/models"
)

// HTTPEmitter posts each event to the Orchestrator DO at
// {WorkerURL}/runs/{RunID}/event. A single background goroutine drains a buffered
// queue, so events are delivered in order; Emit never blocks the engine (a full
// queue drops the event — the seq number lets the consumer notice the gap), and a
// delivery error is swallowed. Flush closes the queue and waits for in-flight
// posts; the emitter is single-use (one run).
type HTTPEmitter struct {
	workerURL string
	runID     string
	client    *http.Client

	ch   chan models.Event
	done chan struct{}

	mu  sync.Mutex
	seq int

	closeOnce sync.Once
}

// DefaultQueue is the buffered depth before Emit starts dropping (best-effort).
const DefaultQueue = 512

// NewHTTPEmitter builds an emitter targeting workerURL for runID. A blank
// workerURL yields a working emitter that simply drops everything (the no-op
// path when no dashboard is configured). runID defaults to "run" to match the
// worker's own fallback.
func NewHTTPEmitter(workerURL, runID string) *HTTPEmitter {
	if runID == "" {
		runID = "run"
	}
	e := &HTTPEmitter{
		workerURL: strings.TrimRight(workerURL, "/"),
		runID:     runID,
		client:    &http.Client{Timeout: 5 * time.Second},
		ch:        make(chan models.Event, DefaultQueue),
		done:      make(chan struct{}),
	}
	go e.loop()
	return e
}

// DashboardURL is the human-facing live view, or "" when no worker is configured.
func (e *HTTPEmitter) DashboardURL() string {
	if e.workerURL == "" {
		return ""
	}
	return e.workerURL + "/?run=" + e.runID
}

// Emit stamps the next sequence number and enqueues the event. It never blocks:
// if the queue is full the event is dropped.
func (e *HTTPEmitter) Emit(ev models.Event) {
	e.mu.Lock()
	e.seq++
	ev.Seq = e.seq
	e.mu.Unlock()

	select {
	case e.ch <- ev:
	default: // queue full — drop; the gap in seq numbers is the signal
	}
}

// Flush stops accepting events and waits for the queue to drain. Safe to call
// more than once.
func (e *HTTPEmitter) Flush() error {
	e.closeOnce.Do(func() { close(e.ch) })
	<-e.done
	return nil
}

func (e *HTTPEmitter) loop() {
	defer close(e.done)
	for ev := range e.ch {
		e.post(ev)
	}
}

func (e *HTTPEmitter) post(ev models.Event) {
	if e.workerURL == "" {
		return
	}
	body, err := json.Marshal(ev)
	if err != nil {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodPost,
		e.workerURL+"/runs/"+e.runID+"/event", bytes.NewReader(body))
	if err != nil {
		return
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := e.client.Do(req)
	if err != nil {
		return // best-effort: the pipeline runs on without the live view
	}
	_ = resp.Body.Close()
}

var _ models.Emitter = (*HTTPEmitter)(nil)

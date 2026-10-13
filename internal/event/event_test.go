package event

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"

	"github.com/KC1706/braid/pkg/models"
)

// recorder is a stand-in Orchestrator DO: it records the path and body of every
// POST /runs/:id/event it receives.
type recorder struct {
	mu     sync.Mutex
	paths  []string
	events []models.Event
}

func (r *recorder) handler() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		body, _ := io.ReadAll(req.Body)
		var ev models.Event
		_ = json.Unmarshal(body, &ev)
		r.mu.Lock()
		r.paths = append(r.paths, req.URL.Path)
		r.events = append(r.events, ev)
		r.mu.Unlock()
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"ok":true}`))
	})
}

func (r *recorder) snapshot() ([]string, []models.Event) {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]string(nil), r.paths...), append([]models.Event(nil), r.events...)
}

func TestHTTPEmitterDeliversInOrderWithSeq(t *testing.T) {
	rec := &recorder{}
	srv := httptest.NewServer(rec.handler())
	defer srv.Close()

	em := NewHTTPEmitter(srv.URL, "run-42")
	em.Emit(models.Event{Type: "run:init", Task: "t"})
	em.Emit(models.Event{Type: "merge:start", Agent: "agent-1"})
	em.Emit(models.Event{Type: "run:done", MergedSHA: "abc"})
	if err := em.Flush(); err != nil {
		t.Fatal(err)
	}

	paths, events := rec.snapshot()
	if len(events) != 3 {
		t.Fatalf("want 3 events delivered, got %d", len(events))
	}
	for _, p := range paths {
		if p != "/runs/run-42/event" {
			t.Errorf("unexpected path %q", p)
		}
	}
	wantTypes := []string{"run:init", "merge:start", "run:done"}
	for i, e := range events {
		if e.Type != wantTypes[i] {
			t.Errorf("event %d type = %q, want %q (order not preserved)", i, e.Type, wantTypes[i])
		}
		if e.Seq != i+1 {
			t.Errorf("event %d seq = %d, want %d (monotonic from 1)", i, e.Seq, i+1)
		}
	}
}

func TestHTTPEmitterBestEffortOnUnreachable(t *testing.T) {
	// A worker that is down must not block or fail the caller.
	em := NewHTTPEmitter("http://127.0.0.1:0", "run-x")
	em.Emit(models.Event{Type: "log", Message: "hi"})
	em.Emit(models.Event{Type: "log", Message: "there"})
	if err := em.Flush(); err != nil {
		t.Fatalf("Flush must swallow delivery errors: %v", err)
	}
}

func TestHTTPEmitterBlankURLIsNoOp(t *testing.T) {
	em := NewHTTPEmitter("", "")
	if d := em.DashboardURL(); d != "" {
		t.Errorf("blank worker URL should have no dashboard URL, got %q", d)
	}
	em.Emit(models.Event{Type: "log", Message: "dropped"})
	if err := em.Flush(); err != nil {
		t.Fatal(err)
	}
}

func TestHTTPEmitterDashboardURL(t *testing.T) {
	em := NewHTTPEmitter("https://w.example.com/", "r1")
	if got, want := em.DashboardURL(), "https://w.example.com/?run=r1"; got != want {
		t.Errorf("DashboardURL = %q, want %q", got, want)
	}
}

func TestHTTPEmitterFlushIsIdempotent(t *testing.T) {
	em := NewHTTPEmitter("", "r")
	if err := em.Flush(); err != nil {
		t.Fatal(err)
	}
	if err := em.Flush(); err != nil {
		t.Fatalf("second Flush should be safe: %v", err)
	}
}

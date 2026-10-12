package resolver

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/KC1706/braid/internal/merge"
	"github.com/KC1706/braid/pkg/models"
)

// The canonical getUser hunk from the TS suite: both sides add a distinct line
// before the shared `return db.find(id);`.
const sample = `export function getUser(id) {
<<<<<<< ours
  validateId(id);
  return db.find(id);
||||||| base
  return db.find(id);
=======
  rateLimit(id);
  return db.find(id);
>>>>>>> theirs
}`

func sampleHunk(t *testing.T) models.ConflictHunk {
	t.Helper()
	hs := merge.ParseConflicts(sample)
	if len(hs) != 1 {
		t.Fatalf("want 1 hunk, got %d", len(hs))
	}
	return hs[0]
}

func TestUnionResolverKeepsBothAdditions(t *testing.T) {
	out, err := UnionResolver{}.Resolve(context.Background(), ResolveInput{
		Path: "src/users.ts", Hunk: sampleHunk(t),
		OursIntent: "input validation", TheirsIntent: "rate limiting",
	})
	if err != nil {
		t.Fatal(err)
	}
	if out.Strategy != "union" {
		t.Errorf("strategy = %q", out.Strategy)
	}
	// Exact parity with the TS union test expectation.
	want := "  validateId(id);\n  rateLimit(id);\n  return db.find(id);"
	if out.Content != want {
		t.Errorf("union content =\n%q\nwant\n%q", out.Content, want)
	}
}

// stubLLM is a configurable OptionalResolver for routing tests.
type stubLLM struct {
	available bool
	called    bool
}

func (s *stubLLM) Name() string    { return "stub" }
func (s *stubLLM) Available() bool { return s.available }
func (s *stubLLM) Resolve(_ context.Context, _ ResolveInput) (ResolveOutput, error) {
	s.called = true
	return ResolveOutput{Content: "llm-content", Strategy: "llm"}, nil
}

func TestHybridUsesUnionOnFirstAttempt(t *testing.T) {
	llm := &stubLLM{available: true}
	h := HybridResolver{LLM: llm}
	out, err := h.Resolve(context.Background(), ResolveInput{Hunk: sampleHunk(t), Attempt: 0})
	if err != nil {
		t.Fatal(err)
	}
	if out.Strategy != "union" {
		t.Errorf("attempt 0 should be union, got %q", out.Strategy)
	}
	if llm.called {
		t.Error("LLM must not be called on the first attempt")
	}
}

func TestHybridEscalatesToLLMOnRetry(t *testing.T) {
	llm := &stubLLM{available: true}
	h := HybridResolver{LLM: llm}
	out, err := h.Resolve(context.Background(), ResolveInput{Hunk: sampleHunk(t), Attempt: 1})
	if err != nil {
		t.Fatal(err)
	}
	if out.Strategy != "llm" || !llm.called {
		t.Errorf("retry should escalate to the LLM; strategy=%q called=%v", out.Strategy, llm.called)
	}
}

func TestHybridFallsBackToUnionWhenLLMUnavailable(t *testing.T) {
	llm := &stubLLM{available: false}
	h := HybridResolver{LLM: llm}
	out, _ := h.Resolve(context.Background(), ResolveInput{Hunk: sampleHunk(t), Attempt: 3})
	if out.Strategy != "union" || llm.called {
		t.Errorf("no available LLM → union; strategy=%q called=%v", out.Strategy, llm.called)
	}
}

func TestWorkersAIResolverHTTP(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/resolve" {
			t.Errorf("unexpected path %q", r.URL.Path)
		}
		w.Header().Set("content-type", "application/json")
		_, _ = w.Write([]byte(`{"content":"  validateId(id);\n  rateLimit(id);\n  return db.find(id);"}`))
	}))
	defer srv.Close()

	r := NewWorkersAIResolver(srv.URL)
	if !r.Available() {
		t.Fatal("should be available with a URL")
	}
	out, err := r.Resolve(context.Background(), ResolveInput{
		Hunk: sampleHunk(t), OursIntent: "a", TheirsIntent: "b",
	})
	if err != nil {
		t.Fatal(err)
	}
	if out.Strategy != "llm" {
		t.Errorf("strategy = %q", out.Strategy)
	}
	if out.Content != "  validateId(id);\n  rateLimit(id);\n  return db.find(id);" {
		t.Errorf("content = %q", out.Content)
	}
}

func TestWorkersAIUnavailableWithoutURL(t *testing.T) {
	t.Setenv("BRAID_WORKER_URL", "")
	if NewWorkersAIResolver("").Available() {
		t.Error("no URL → unavailable")
	}
}

func TestAutoLLMResolverSelection(t *testing.T) {
	// No config → none.
	t.Setenv("ANTHROPIC_API_KEY", "")
	t.Setenv("BRAID_WORKER_URL", "")
	if _, ok := AutoLLMResolver(); ok {
		t.Error("no config should yield no resolver")
	}
	// Worker URL only → Workers AI.
	t.Setenv("BRAID_WORKER_URL", "https://worker.example")
	r, ok := AutoLLMResolver()
	if !ok || r.Name() != "workers-ai" {
		t.Errorf("want workers-ai, got ok=%v name=%v", ok, r)
	}
	// Anthropic key present → Claude wins.
	t.Setenv("ANTHROPIC_API_KEY", "sk-test")
	r, ok = AutoLLMResolver()
	if !ok || r.Name() != "claude" {
		t.Errorf("want claude, got ok=%v name=%v", ok, r)
	}
}

func TestStripFences(t *testing.T) {
	cases := map[string]string{
		"```go\ncode\n```": "code",
		"plain":            "plain",
		"```\nx\n```":      "x",
	}
	for in, want := range cases {
		if got := stripFences(in); got != want {
			t.Errorf("stripFences(%q) = %q, want %q", in, got, want)
		}
	}
}

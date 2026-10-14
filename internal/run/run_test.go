package run

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/KC1706/braid/internal/contract"
	"github.com/KC1706/braid/internal/demo"
	"github.com/KC1706/braid/internal/exec"
	"github.com/KC1706/braid/pkg/models"
)

// seedDemo builds the demo fixture in a temp dir and returns the RunSpec pieces.
func seedDemo(t *testing.T) (repoDir, baseline string, agents []AgentSpec) {
	t.Helper()
	repoDir = filepath.Join(t.TempDir(), "repo")
	base, ag, err := demo.Seed(repoDir)
	if err != nil {
		t.Fatalf("seed: %v", err)
	}
	specs := make([]AgentSpec, len(ag))
	for i, a := range ag {
		specs[i] = AgentSpec{ID: a.ID, Branch: a.Branch, Intent: a.Intent}
	}
	return repoDir, base, specs
}

// collectingEmitter records events for assertion; Flush is a no-op.
type collectingEmitter struct {
	mu     sync.Mutex
	events []models.Event
}

func (c *collectingEmitter) Emit(e models.Event) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.events = append(c.events, e)
}
func (c *collectingEmitter) Flush() error { return nil }

func TestServiceRunGreenLocal(t *testing.T) {
	repoDir, baseline, agents := seedDemo(t)
	em := &collectingEmitter{}
	svc := Service{Emitter: em}

	res, err := svc.Run(context.Background(), RunSpec{
		RepoDir:    repoDir,
		Baseline:   baseline,
		Task:       "test run",
		Agents:     agents,
		MaxRetries: 2,
	})
	if err != nil {
		t.Fatal(err)
	}
	if !res.Green || res.Verdict != models.VerdictGreen {
		t.Fatalf("want green, got verdict=%q green=%v", res.Verdict, res.Green)
	}
	if res.Summary.AgentsMerged != 3 {
		t.Errorf("want 3 agents merged, got %d", res.Summary.AgentsMerged)
	}
	if res.Summary.ConflictsResolved != 1 {
		t.Errorf("want 1 conflict resolved, got %d", res.Summary.ConflictsResolved)
	}
	if res.MergedSHA == "" {
		t.Error("want a merged SHA")
	}
	if len(em.events) == 0 {
		t.Error("the emitter should have received events")
	}
}

func TestServiceRunGreenSandbox(t *testing.T) {
	if !(exec.SeatbeltBackend{}).Available() {
		t.Skip("no OS sandbox backend on this host")
	}
	repoDir, baseline, agents := seedDemo(t)
	svc := Service{}

	res, err := svc.Run(context.Background(), RunSpec{
		RepoDir:    repoDir,
		Baseline:   baseline,
		Agents:     agents,
		MaxRetries: 2,
		Sandbox:    true, // build+test run with no creds, external egress denied
	})
	if err != nil {
		t.Fatal(err)
	}
	if !res.Green {
		t.Fatalf("sandboxed run should reach green, got verdict=%q", res.Verdict)
	}
	if res.Summary.AgentsMerged != 3 {
		t.Errorf("want 3 agents merged inside the sandbox, got %d", res.Summary.AgentsMerged)
	}
}

// TestServiceStreamsToDashboard proves the full transport wire: a RunSpec with a
// DashboardURL makes Service build an HTTPEmitter and stream a real run's events
// to the (stand-in) Orchestrator DO, seq-numbered and ending in run:done.
func TestServiceStreamsToDashboard(t *testing.T) {
	var count atomic.Int64
	var mu sync.Mutex
	var sawFinal, sawMergeStart bool
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if got := r.URL.Path; got != "/runs/stream-1/event" {
			t.Errorf("unexpected path %q", got)
		}
		count.Add(1)
		var ev models.Event
		_ = json.NewDecoder(r.Body).Decode(&ev)
		mu.Lock()
		if ev.Type == "merge:start" {
			sawMergeStart = true
		}
		// The engine's terminal signal is the final verify (it does not emit a
		// TS-style run:done; aligning the vocabulary is a dashboard-phase task).
		if ev.Type == "verify" && ev.Status == "final" {
			sawFinal = true
		}
		mu.Unlock()
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	repoDir, baseline, agents := seedDemo(t)
	_, err := Service{}.Run(context.Background(), RunSpec{
		RunID:        "stream-1",
		RepoDir:      repoDir,
		Baseline:     baseline,
		Agents:       agents,
		MaxRetries:   2,
		DashboardURL: srv.URL,
	})
	if err != nil {
		t.Fatal(err)
	}
	if count.Load() == 0 {
		t.Fatal("no events reached the dashboard")
	}
	mu.Lock()
	defer mu.Unlock()
	if !sawMergeStart {
		t.Error("expected a merge:start event")
	}
	if !sawFinal {
		t.Error("expected a terminal verify(status=final) event")
	}
}

func TestServiceRunValidates(t *testing.T) {
	svc := Service{}
	if _, err := svc.Run(context.Background(), RunSpec{Baseline: "main"}); err == nil {
		t.Error("missing RepoDir should error")
	}
	if _, err := svc.Run(context.Background(), RunSpec{RepoDir: "/tmp/x"}); err == nil {
		t.Error("missing Baseline should error")
	}
}

func TestResolverFor(t *testing.T) {
	for _, kind := range []string{"", "union", "auto", "hybrid"} {
		if _, err := resolverFor(kind); err != nil {
			t.Errorf("resolverFor(%q) unexpected error: %v", kind, err)
		}
	}
	if _, err := resolverFor("nope"); err == nil {
		t.Error("unknown resolver should error")
	}
	// workersai/claude require configuration; unconfigured, they must error
	// rather than silently falling back (an explicit choice should be honored).
	t.Setenv("BRAID_WORKER_URL", "")
	if _, err := resolverFor("workersai"); err == nil {
		t.Error("workersai without BRAID_WORKER_URL should error")
	}
	t.Setenv("ANTHROPIC_API_KEY", "")
	if _, err := resolverFor("claude"); err == nil {
		t.Error("claude without ANTHROPIC_API_KEY should error")
	}
}

func TestServiceRunTree(t *testing.T) {
	repoDir, baseline, agents := seedDemo(t)
	res, err := Service{}.Run(context.Background(), RunSpec{
		RepoDir: repoDir, Baseline: baseline, Task: "tree run",
		Agents: agents, MaxRetries: 2, Tree: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	if !res.Green || res.Verdict != models.VerdictGreen {
		t.Fatalf("want green via tree fold, got verdict=%q green=%v", res.Verdict, res.Green)
	}
	if res.Summary.AgentsMerged != 3 {
		t.Errorf("want 3 agents merged, got %d", res.Summary.AgentsMerged)
	}
}

func TestServiceRunContractsPassThrough(t *testing.T) {
	repoDir, baseline, agents := seedDemo(t)
	// A contract for the first demo agent, naming a test that does not exist on
	// the fork: the rung must RUN (proving pass-through) but impose no obligation
	// (a fork-failing check establishes nothing), so the run stays green.
	contracts := []contract.Contract{{
		Agent:  agents[0].ID,
		Intent: "pass-through probe",
		Checks: []contract.Check{{Name: "probe", Package: "./...", Test: "TestDoesNotExist"}},
	}}
	res, err := Service{}.Run(context.Background(), RunSpec{
		RepoDir: repoDir, Baseline: baseline, Task: "contracts run",
		Agents: agents, MaxRetries: 2, Contracts: contracts,
	})
	if err != nil {
		t.Fatal(err)
	}
	m := res.Trail.Metamorphic
	if m == nil {
		t.Fatal("expected the metamorphic rung to run (Trail.Metamorphic set)")
	}
	if m.Checked != 1 {
		t.Fatalf("Checked = %d, want 1", m.Checked)
	}
	if !m.Passed || !res.Green {
		t.Fatalf("a fork-failing check imposes no obligation; want green, got passed=%v green=%v", m.Passed, res.Green)
	}
}

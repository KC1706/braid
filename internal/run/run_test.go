package run

import (
	"context"
	"path/filepath"
	"sync"
	"testing"

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

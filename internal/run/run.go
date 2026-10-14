// Package run is the service layer around the merge engine: it turns a declarative
// RunSpec into a merge.Options, chooses the executor (local or sandboxed) and the
// resolver, wires the event stream, calls Integrate, and returns a structured
// RunResult. It is the single code path both `cmd/braid demo` and `cmd/braid
// serve` go through, and the thing the control-plane Worker will call later
// (GO_PRODUCTION_PLAN.md Phase 1). It owns no transport and no auth — those wrap
// it — and it never moves credentials: the engine runs untrusted build/test work
// through the sandbox executor.
package run

import (
	"context"
	"fmt"

	"github.com/KC1706/braid/internal/contract"
	"github.com/KC1706/braid/internal/event"
	"github.com/KC1706/braid/internal/exec"
	"github.com/KC1706/braid/internal/ladder"
	"github.com/KC1706/braid/internal/merge"
	"github.com/KC1706/braid/internal/resolver"
	"github.com/KC1706/braid/pkg/models"
)

// AgentSpec is one agent branch to integrate, in declarative (transport) form.
type AgentSpec struct {
	ID     string `json:"id"`
	Branch string `json:"branch"`
	Intent string `json:"intent"`
}

// RunSpec is a declarative integration request — the wire shape a client (CLI,
// Worker) sends. It names a prepared repo (baseline + agent branches already
// present), not raw code; forking/cloning is the control plane's job.
type RunSpec struct {
	RunID             string      `json:"runId,omitempty"`
	RepoDir           string      `json:"repoDir"`
	Baseline          string      `json:"baseline"`
	IntegrationBranch string      `json:"integrationBranch,omitempty"`
	Task              string      `json:"task,omitempty"`
	Agents            []AgentSpec `json:"agents"`
	MaxRetries        int         `json:"maxRetries,omitempty"`
	// Resolver selects conflict resolution: "union" (default, deterministic),
	// "workersai", "claude", "hybrid", or "auto" (union + whatever LLM is configured).
	Resolver string `json:"resolver,omitempty"`
	// Sandbox runs build+test through exec.SandboxExecutor (no creds, egress denied)
	// instead of the host process.
	Sandbox bool `json:"sandbox,omitempty"`
	// Vet adds `go vet` to the build rung.
	Vet bool `json:"vet,omitempty"`
	// Tree folds the agents as a pairwise tree of depth O(log N) (2e) instead of
	// the linear fold; the full suite still runs once at the root.
	Tree bool `json:"tree,omitempty"`
	// Contracts, when present, enable the Rung-5 metamorphic gate: each agent's
	// acceptance contract (keyed to its agent id) is snapshotted on its own fork
	// and re-checked on the merged tree (2b/2c). Empty → the rung is skipped.
	Contracts []contract.Contract `json:"contracts,omitempty"`
	// DashboardURL, when set (and no Emitter is injected on the Service), streams
	// the live event feed to the Orchestrator DO at that worker base URL.
	DashboardURL string `json:"dashboardUrl,omitempty"`
}

// RunResult is the structured outcome of a run.
type RunResult struct {
	RunID     string              `json:"runId,omitempty"`
	Verdict   models.Verdict      `json:"verdict"`
	Green     bool                `json:"green"`
	MergedSHA string              `json:"mergedSha"`
	Summary   models.TrailSummary `json:"summary"`
	Trail     models.Trail        `json:"trail"`
}

// Service runs integrations. A zero Service is usable: Log and Emitter default
// to no-ops, so callers wire only what they need.
type Service struct {
	// Emitter, when set, receives the live event stream; Flush is called at the
	// end of each run. nil = events are dropped.
	Emitter models.Emitter
	// Log, when set, receives human-readable progress lines. nil = silent.
	Log func(string)
}

// Run executes one integration described by spec.
func (s Service) Run(ctx context.Context, spec RunSpec) (RunResult, error) {
	if spec.RepoDir == "" {
		return RunResult{}, fmt.Errorf("run: RepoDir is required")
	}
	if spec.Baseline == "" {
		return RunResult{}, fmt.Errorf("run: Baseline is required")
	}

	res, err := resolverForCached(spec.Resolver)
	if err != nil {
		return RunResult{}, err
	}

	var executor exec.Executor = exec.LocalExecutor{}
	if spec.Sandbox {
		executor = exec.SandboxExecutor{}
	}
	lad := &ladder.Ladder{
		Build: ladder.GoBuildVerifier{VetToo: spec.Vet, Exec: executor},
		Test:  ladder.GoTestVerifier{Exec: executor},
	}

	agents := make([]merge.AgentInput, len(spec.Agents))
	for i, a := range spec.Agents {
		agents[i] = merge.AgentInput{ID: a.ID, Branch: a.Branch, Intent: a.Intent}
	}

	log := s.Log
	if log == nil {
		log = func(string) {}
	}

	// Pick the event sink: an explicitly injected Emitter wins (tests, custom
	// transports); otherwise a DashboardURL builds a per-run HTTP emitter. Either
	// way Run flushes it before returning so a short-lived caller loses nothing.
	emitter := s.Emitter
	if emitter == nil && spec.DashboardURL != "" {
		emitter = event.NewHTTPEmitter(spec.DashboardURL, spec.RunID)
	}
	var emit func(models.Event)
	if emitter != nil {
		emit = emitter.Emit
	}

	opts := merge.Options{
		RepoDir:           spec.RepoDir,
		Baseline:          spec.Baseline,
		IntegrationBranch: spec.IntegrationBranch,
		Task:              spec.Task,
		Agents:            agents,
		Resolver:          res,
		Ladder:            lad,
		MaxRetries:        spec.MaxRetries,
		CanonicalOrder:    true, // production runs fold agents deterministically (2d)
		Contracts:         spec.Contracts,
		Log:               log,
		Emit:              emit,
	}
	integrate := merge.Integrate
	if spec.Tree {
		integrate = merge.IntegrateTree
	}
	out, err := integrate(ctx, opts)
	if emitter != nil {
		// Best-effort: deliver any buffered events before returning.
		_ = emitter.Flush()
	}
	if err != nil {
		return RunResult{}, err
	}

	return RunResult{
		RunID:     spec.RunID,
		Verdict:   out.Verdict,
		Green:     out.Green,
		MergedSHA: out.MergedSHA,
		Summary:   out.Trail.Summary,
		Trail:     out.Trail,
	}, nil
}

// resolverForCached builds the selected resolver and wraps it in a CachingResolver
// so identical conflicts resolve to identical bytes within and across runs (2d) —
// the determinism the LLM path otherwise lacks, and a free win for the union path.
func resolverForCached(kind string) (resolver.Resolver, error) {
	r, err := resolverFor(kind)
	if err != nil {
		return nil, err
	}
	return resolver.NewCachingResolver(r, resolver.PromptVersion), nil
}

// resolverFor maps a RunSpec.Resolver string to a resolver. "" and "union" are
// the deterministic default; "auto"/"hybrid" layer whatever LLM is configured on
// top of union; "workersai"/"claude" force that backend (erroring if unconfigured).
func resolverFor(kind string) (resolver.Resolver, error) {
	switch kind {
	case "", "union":
		return resolver.UnionResolver{}, nil
	case "auto", "hybrid":
		llm, _ := resolver.AutoLLMResolver() // nil → union-only, which is fine
		return resolver.HybridResolver{Union: resolver.UnionResolver{}, LLM: llm}, nil
	case "workersai":
		w := resolver.NewWorkersAIResolver("")
		if !w.Available() {
			return nil, fmt.Errorf("run: resolver %q requires BRAID_WORKER_URL", kind)
		}
		return resolver.HybridResolver{Union: resolver.UnionResolver{}, LLM: w}, nil
	case "claude":
		c := resolver.NewClaudeResolver()
		if !c.Available() {
			return nil, fmt.Errorf("run: resolver %q requires ANTHROPIC_API_KEY", kind)
		}
		return resolver.HybridResolver{Union: resolver.UnionResolver{}, LLM: c}, nil
	default:
		return nil, fmt.Errorf("run: unknown resolver %q", kind)
	}
}

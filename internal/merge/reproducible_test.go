package merge

import (
	"context"
	"testing"

	"github.com/KC1706/braid/internal/determinism"
	"github.com/KC1706/braid/internal/resolver"
)

// buildUserRepo seeds the canonical 3-agent fixture (agent-1/agent-2 both edit
// getUser → one true conflict; agent-3 adds a clean file) in a fresh repo and
// returns the dir. Shared by the reproducibility runs below.
func buildUserRepo(t *testing.T) string {
	t.Helper()
	dir := initRepo(t)
	commitBaseline(t, dir, map[string]string{
		"go.mod":      "module example.com/app\n\ngo 1.26\n",
		"app.go":      appBase,
		"app_test.go": appTest,
	})
	makeBranch(t, dir, "agent/validation", map[string]string{"app.go": appValidation})
	makeBranch(t, dir, "agent/ratelimit", map[string]string{"app.go": appRatelimit})
	makeBranch(t, dir, "agent/docs", map[string]string{
		"doc.go": "package app\n\n// DocVersion added by agent-3.\nvar DocVersion = \"1\"\n",
	})
	return dir
}

func integrateFor(t *testing.T, dir string, agents []AgentInput) string {
	t.Helper()
	res, err := Integrate(context.Background(), Options{
		RepoDir: dir, Baseline: "baseline", Task: "user work",
		Agents:         agents,
		Resolver:       resolver.NewCachingResolver(resolver.UnionResolver{}, resolver.PromptVersion),
		Ladder:         newLadder(),
		MaxRetries:     2,
		CanonicalOrder: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	if !res.Green {
		t.Fatalf("expected green, got verdict=%q", res.Verdict)
	}
	// Fingerprint the merged SOURCE tree, excluding the provenance artifact
	// (.braid/trail.json embeds timestamps/durations → not reproducible, and not
	// part of "the merge result").
	tree, err := determinism.FingerprintTree(dir, "HEAD", ".braid/")
	if err != nil {
		t.Fatal(err)
	}
	return tree
}

// Identical inputs supplied in DIFFERENT orders must fold to the identical merged
// tree (GO_PRODUCTION_PLAN.md Phase 2 exit gate: reproducibility). The tree hash
// is content-only, so this proves the merged files are byte-identical regardless
// of commit metadata or the order the agents arrived in.
func TestReproducibleMergeAcrossAgentOrder(t *testing.T) {
	order1 := []AgentInput{
		{ID: "agent-1", Branch: "agent/validation", Intent: "input validation"},
		{ID: "agent-2", Branch: "agent/ratelimit", Intent: "rate limiting"},
		{ID: "agent-3", Branch: "agent/docs", Intent: "documentation"},
	}
	order2 := []AgentInput{
		{ID: "agent-3", Branch: "agent/docs", Intent: "documentation"},
		{ID: "agent-1", Branch: "agent/validation", Intent: "input validation"},
		{ID: "agent-2", Branch: "agent/ratelimit", Intent: "rate limiting"},
	}

	// Two independent repos (independent commit timestamps/SHAs) from identical
	// seeds, folded in two different agent orders.
	h1 := integrateFor(t, buildUserRepo(t), order1)
	h2 := integrateFor(t, buildUserRepo(t), order2)

	if h1 != h2 {
		t.Fatalf("non-reproducible merge: tree %s != %s", h1, h2)
	}

	// And a third run of the first order reproduces the same hash (determinism
	// across repeated runs, not just across permutations).
	h3 := integrateFor(t, buildUserRepo(t), order1)
	if h3 != h1 {
		t.Fatalf("merge not reproducible across runs: tree %s != %s", h3, h1)
	}
}

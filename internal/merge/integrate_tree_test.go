package merge

import (
	"context"
	"testing"

	"github.com/KC1706/braid/internal/determinism"
	"github.com/KC1706/braid/internal/resolver"
)

// integrateTreeFor runs the tree fold and returns the merged source-tree
// fingerprint (excluding the non-deterministic .braid/ provenance artifact).
func integrateTreeFor(t *testing.T, dir string, agents []AgentInput) (Result, string) {
	t.Helper()
	res, err := IntegrateTree(context.Background(), Options{
		RepoDir: dir, Baseline: "baseline", Task: "user work",
		Agents:     agents,
		Resolver:   resolver.NewCachingResolver(resolver.UnionResolver{}, resolver.PromptVersion),
		Ladder:     newLadder(),
		MaxRetries: 2,
	})
	if err != nil {
		t.Fatal(err)
	}
	fp, err := determinism.FingerprintTree(dir, "HEAD", ".braid/")
	if err != nil {
		t.Fatal(err)
	}
	return res, fp
}

func threeUserAgents() []AgentInput {
	return []AgentInput{
		{ID: "agent-1", Branch: "agent/validation", Intent: "input validation"},
		{ID: "agent-2", Branch: "agent/ratelimit", Intent: "rate limiting"},
		{ID: "agent-3", Branch: "agent/docs", Intent: "documentation"},
	}
}

// The tree fold composes the 3-agent fixture green, with the one true conflict
// resolved by the deterministic union — the same outcome as the linear fold.
func TestIntegrateTreeComposesGreen(t *testing.T) {
	res, _ := integrateTreeFor(t, buildUserRepo(t), threeUserAgents())
	if !res.Green || res.Verdict != "green" {
		t.Fatalf("want green, got verdict=%q green=%v (%s)", res.Verdict, res.Green, res.Trail.FinalTest)
	}
	if res.Trail.Summary.AgentsMerged != 3 {
		t.Errorf("agentsMerged = %d, want 3", res.Trail.Summary.AgentsMerged)
	}
	if len(res.Trail.Conflicts) != 1 {
		t.Fatalf("want 1 conflict, got %d", len(res.Trail.Conflicts))
	}
	if res.Trail.Conflicts[0].Strategy != "union" {
		t.Errorf("strategy = %q, want union", res.Trail.Conflicts[0].Strategy)
	}
}

// For n=3 the balanced tree has the same associativity as the linear fold, so the
// tree merge must produce the byte-identical merged source tree. This pins the
// tree fold as a correct, equivalent alternative to the proven linear path.
func TestIntegrateTreeEquivalentToLinearForThree(t *testing.T) {
	_, treeFP := integrateTreeFor(t, buildUserRepo(t), threeUserAgents())
	linearFP := integrateFor(t, buildUserRepo(t), threeUserAgents())
	if treeFP != linearFP {
		t.Fatalf("tree fold diverged from linear fold:\n  tree   = %s\n  linear = %s", treeFP, linearFP)
	}
}

// A 4-agent fixture exercises a balanced depth-2 tree merge(merge(0,1),merge(2,3))
// with the true conflict surfacing at the ROOT — and it must be reproducible.
func TestIntegrateTreeFourAgentsBalancedAndReproducible(t *testing.T) {
	build := func(t *testing.T) string {
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
		makeBranch(t, dir, "agent/meta", map[string]string{
			"meta.go": "package app\n\n// Meta added by agent-4.\nvar Meta = \"m\"\n",
		})
		return dir
	}
	agents := []AgentInput{
		{ID: "agent-1", Branch: "agent/validation", Intent: "input validation"},
		{ID: "agent-4", Branch: "agent/meta", Intent: "metadata"},
		{ID: "agent-2", Branch: "agent/ratelimit", Intent: "rate limiting"},
		{ID: "agent-3", Branch: "agent/docs", Intent: "documentation"},
	}

	res, fp1 := integrateTreeFor(t, build(t), agents)
	if !res.Green {
		t.Fatalf("4-agent tree fold not green: verdict=%q (%s)", res.Verdict, res.Trail.FinalTest)
	}
	if res.Trail.Summary.AgentsMerged != 4 {
		t.Errorf("agentsMerged = %d, want 4", res.Trail.Summary.AgentsMerged)
	}
	// Reproducible: a second run (and a permuted input order) yields the same tree.
	_, fp2 := integrateTreeFor(t, build(t), agents)
	if fp1 != fp2 {
		t.Fatalf("4-agent tree fold not reproducible: %s != %s", fp1, fp2)
	}
	permuted := []AgentInput{agents[3], agents[0], agents[2], agents[1]}
	_, fp3 := integrateTreeFor(t, build(t), permuted)
	if fp1 != fp3 {
		t.Fatalf("4-agent tree fold not order-independent: %s != %s", fp1, fp3)
	}
}

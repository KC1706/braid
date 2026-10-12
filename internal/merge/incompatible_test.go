package merge

import (
	"context"
	"testing"

	"github.com/KC1706/braid/internal/policy"
	"github.com/KC1706/braid/internal/resolver"
)

const priceBase = `package price

const Rate = 1

func Price(n int) int { return n * Rate }
`

const priceTest = `package price

import "testing"

func TestPrice(t *testing.T) {
	if Price(10) != 10 {
		t.Fatalf("want 10, got %d", Price(10))
	}
}
`

// buildPriceRepo: baseline + agent-a (docs-only, compatible) + agent-b (doubles
// the rate, breaking the frozen test). agent-b composes with NO textual conflict
// yet breaks verification — the §5.1 "union is not a law" case (C4).
func buildPriceRepo(t *testing.T) (dir string, agents []AgentInput) {
	t.Helper()
	dir = initRepo(t)
	commitBaseline(t, dir, map[string]string{
		"go.mod":        "module example.com/price\n\ngo 1.26\n",
		"price.go":      priceBase,
		"price_test.go": priceTest,
		"README.md":     "# target\n",
	})
	makeBranch(t, dir, "agent/docs", map[string]string{"README.md": "# target\n\nNow documented.\n"})
	makeBranch(t, dir, "agent/rate", map[string]string{
		"price.go": "package price\n\nconst Rate = 2\n\nfunc Price(n int) int { return n * Rate }\n",
	})
	agents = []AgentInput{
		{ID: "agent-a", Branch: "agent/docs", Intent: "documentation"},
		{ID: "agent-b", Branch: "agent/rate", Intent: "double the rate"},
	}
	return dir, agents
}

func TestIncompatibleDefaultEscalates(t *testing.T) {
	dir, agents := buildPriceRepo(t)
	res, err := Integrate(context.Background(), Options{
		RepoDir: dir, Baseline: "baseline", Task: "price work",
		Agents: agents, Resolver: resolver.UnionResolver{}, Ladder: newLadder(), MaxRetries: 0,
		// default policy = EscalatePolicy
	})
	if err != nil {
		t.Fatal(err)
	}
	if res.Verdict != "incompatible" {
		t.Fatalf("verdict = %q, want incompatible", res.Verdict)
	}
	if res.Green {
		t.Error("an escalated run is not green")
	}
	if len(res.Trail.Incompatibilities) != 1 {
		t.Fatalf("want 1 incompatibility, got %d", len(res.Trail.Incompatibilities))
	}
	inc := res.Trail.Incompatibilities[0]
	if inc.Agent != "agent-b" || inc.Action != "escalate" {
		t.Errorf("incompatibility = %+v", inc)
	}
	if inc.Dropped != "" {
		t.Errorf("escalate must drop nothing, dropped %q", inc.Dropped)
	}
	// agent-a survived and stays integrated (not dropped).
	for _, a := range res.Trail.Agents {
		if a.Agent == "agent-a" && a.Dropped {
			t.Error("agent-a should not be dropped")
		}
	}
}

func TestIncompatiblePickWinnerDropsLoserAndGreens(t *testing.T) {
	dir, agents := buildPriceRepo(t)
	res, err := Integrate(context.Background(), Options{
		RepoDir: dir, Baseline: "baseline", Task: "price work",
		Agents: agents, Resolver: resolver.UnionResolver{}, Ladder: newLadder(), MaxRetries: 0,
		Policy: policy.PickWinnerPolicy{Order: []string{"agent-a", "agent-b"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if res.Verdict != "green" || !res.Green {
		t.Fatalf("pick-winner should reach green, got verdict=%q green=%v", res.Verdict, res.Green)
	}
	if len(res.Trail.Incompatibilities) != 1 {
		t.Fatalf("want 1 incompatibility, got %d", len(res.Trail.Incompatibilities))
	}
	inc := res.Trail.Incompatibilities[0]
	if inc.Action != "drop" || inc.Dropped != "agent-b" {
		t.Errorf("incompatibility = %+v, want drop agent-b", inc)
	}
	// The dropped agent is recorded, not silently lost.
	var agentB *struct{ dropped bool }
	for _, a := range res.Trail.Agents {
		if a.Agent == "agent-b" {
			agentB = &struct{ dropped bool }{a.Dropped}
		}
	}
	if agentB == nil || !agentB.dropped {
		t.Error("agent-b should be recorded as dropped")
	}
	if res.Trail.Summary.AgentsMerged != 1 {
		t.Errorf("agentsMerged = %d, want 1 (only agent-a)", res.Trail.Summary.AgentsMerged)
	}
}

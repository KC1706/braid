package merge

import (
	"context"
	"strings"
	"testing"

	"github.com/KC1706/braid/internal/resolver"
)

const appBase = `package app

func validate(id string) {}
func rate(id string) {}
func lookup(id string) string { return "user:" + id }

func getUser(id string) string {
	return lookup(id)
}
`

const appValidation = `package app

func validate(id string) {}
func rate(id string) {}
func lookup(id string) string { return "user:" + id }

func getUser(id string) string {
	validate(id)
	return lookup(id)
}
`

const appRatelimit = `package app

func validate(id string) {}
func rate(id string) {}
func lookup(id string) string { return "user:" + id }

func getUser(id string) string {
	rate(id)
	return lookup(id)
}
`

const appTest = `package app

import "testing"

func TestGetUser(t *testing.T) {
	if getUser("x") == "" {
		t.Fatal("empty")
	}
}
`

// Three Go agents compose green through the ladder: agent-1 and agent-2 both edit
// getUser (a true conflict → union keeps both), agent-3 adds docs (clean).
func TestIntegrateThreeAgentsComposeGreen(t *testing.T) {
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

	res, err := Integrate(context.Background(), Options{
		RepoDir: dir, Baseline: "baseline", Task: "user work",
		Agents: []AgentInput{
			{ID: "agent-1", Branch: "agent/validation", Intent: "input validation"},
			{ID: "agent-2", Branch: "agent/ratelimit", Intent: "rate limiting"},
			{ID: "agent-3", Branch: "agent/docs", Intent: "documentation"},
		},
		Resolver: resolver.UnionResolver{}, Ladder: newLadder(), MaxRetries: 2,
	})
	if err != nil {
		t.Fatal(err)
	}
	if !res.Green || res.Verdict != "green" {
		t.Fatalf("want green, got verdict=%q green=%v\n%s", res.Verdict, res.Green, res.Trail.FinalTest)
	}
	if len(res.Trail.Agents) != 3 {
		t.Fatalf("want 3 agent records, got %d", len(res.Trail.Agents))
	}
	if res.Trail.Summary.AgentsMerged != 3 {
		t.Errorf("agentsMerged = %d, want 3", res.Trail.Summary.AgentsMerged)
	}
	// Exactly one conflict, resolved by the deterministic union (no LLM).
	if len(res.Trail.Conflicts) != 1 {
		t.Fatalf("want 1 conflict, got %d", len(res.Trail.Conflicts))
	}
	if res.Trail.Conflicts[0].Strategy != "union" {
		t.Errorf("strategy = %q, want union", res.Trail.Conflicts[0].Strategy)
	}
	if res.Trail.Summary.LLMResolutions != 0 {
		t.Errorf("llmResolutions = %d, want 0", res.Trail.Summary.LLMResolutions)
	}
	// Both features survived in the merged source (compose, don't pick a winner).
	merged := readFileRel(t, dir, "app.go")
	if !strings.Contains(merged, "validate(id)") || !strings.Contains(merged, "rate(id)") {
		t.Errorf("both agents' additions should survive:\n%s", merged)
	}
	// The trail was written and parses.
	tr := readTrail(t, dir)
	if tr.Verdict != "green" {
		t.Errorf("trail verdict = %q", tr.Verdict)
	}
}

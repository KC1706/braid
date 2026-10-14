package merge

import (
	"context"
	"os/exec"
	"testing"

	"github.com/KC1706/braid/internal/contract"
	"github.com/KC1706/braid/internal/resolver"
	"github.com/KC1706/braid/pkg/gitx"
)

// Fixtures for the metamorphic rung: validate/rate record into a package global
// so a test can observe WHICH behavior getUser performed. The baseline defines
// both recorders but calls neither; each agent makes getUser call one of them.
const mmBase = `package app

var calls []string

func resetCalls() { calls = nil }
func called(s string) bool {
	for _, c := range calls {
		if c == s {
			return true
		}
	}
	return false
}

func validate(id string)      { calls = append(calls, "validate") }
func rate(id string)          { calls = append(calls, "rate") }
func lookup(id string) string { return "user:" + id }

func getUser(id string) string {
	return lookup(id)
}
`

const mmValidation = `package app

var calls []string

func resetCalls() { calls = nil }
func called(s string) bool {
	for _, c := range calls {
		if c == s {
			return true
		}
	}
	return false
}

func validate(id string)      { calls = append(calls, "validate") }
func rate(id string)          { calls = append(calls, "rate") }
func lookup(id string) string { return "user:" + id }

func getUser(id string) string {
	validate(id)
	return lookup(id)
}
`

const mmRatelimit = `package app

var calls []string

func resetCalls() { calls = nil }
func called(s string) bool {
	for _, c := range calls {
		if c == s {
			return true
		}
	}
	return false
}

func validate(id string)      { calls = append(calls, "validate") }
func rate(id string)          { calls = append(calls, "rate") }
func lookup(id string) string { return "user:" + id }

func getUser(id string) string {
	rate(id)
	return lookup(id)
}
`

const mmValidationTest = `package app

import "testing"

func TestValidation(t *testing.T) {
	resetCalls()
	getUser("x")
	if !called("validate") {
		t.Fatal("getUser did not validate")
	}
}
`

const mmRatelimitTest = `package app

import "testing"

func TestRatelimit(t *testing.T) {
	resetCalls()
	getUser("x")
	if !called("rate") {
		t.Fatal("getUser did not rate-limit")
	}
}
`

const mmBaseTest = `package app

import "testing"

func TestLookup(t *testing.T) {
	if lookup("x") == "" {
		t.Fatal("empty")
	}
}
`

// The two agents' contracts: each pins the behavior its feature establishes.
func mmContracts() []contract.Contract {
	return []contract.Contract{
		{Agent: "agent-1", Intent: "input validation", Checks: []contract.Check{
			{Name: "validates", Package: "./...", Test: "TestValidation"},
		}},
		{Agent: "agent-2", Intent: "rate limiting", Checks: []contract.Check{
			{Name: "ratelimits", Package: "./...", Test: "TestRatelimit"},
		}},
	}
}

// snapshotForks checks out each agent's own branch and records its contract's
// behavior there (the Rung-5 goldens).
func snapshotForks(t *testing.T, dir string, branches map[string]string, contracts []contract.Contract, r contract.CheckRunner) []contract.Snapshot {
	t.Helper()
	var snaps []contract.Snapshot
	for _, c := range contracts {
		if _, err := gitx.Ok(dir, "checkout", "-q", branches[c.Agent]); err != nil {
			t.Fatal(err)
		}
		s, err := contract.TakeSnapshot(context.Background(), dir, c, r)
		if err != nil {
			t.Fatal(err)
		}
		snaps = append(snaps, s)
	}
	return snaps
}

// TestMetamorphicBothFeaturesSurvive is the Phase-2 exit-gate case for Rung 5:
// agent-1 (validation) and agent-2 (rate limiting) each pass their contract on
// their own fork; after the hybrid union merge, the merged tree STILL satisfies
// BOTH contracts — the metamorphic relation holds, zero violations.
func TestMetamorphicBothFeaturesSurvive(t *testing.T) {
	dir := initRepo(t)
	commitBaseline(t, dir, map[string]string{
		"go.mod":       "module example.com/app\n\ngo 1.26\n",
		"app.go":       mmBase,
		"base_test.go": mmBaseTest,
	})
	makeBranch(t, dir, "agent/validation", map[string]string{"app.go": mmValidation, "validation_test.go": mmValidationTest})
	makeBranch(t, dir, "agent/ratelimit", map[string]string{"app.go": mmRatelimit, "ratelimit_test.go": mmRatelimitTest})

	contracts := mmContracts()
	runner := contract.GoTestRunner{}
	branches := map[string]string{"agent-1": "agent/validation", "agent-2": "agent/ratelimit"}

	// Goldens: each feature passes on its own fork.
	forks := snapshotForks(t, dir, branches, contracts, runner)
	for _, s := range forks {
		for _, r := range s.Results {
			if !r.Passed {
				t.Fatalf("fork %s check %q did not pass on its own branch: %s", s.Agent, r.Check, r.Detail)
			}
		}
	}

	// Merge (union keeps both getUser additions; both test files carry through).
	res, err := Integrate(context.Background(), Options{
		RepoDir: dir, Baseline: "baseline", Task: "metamorphic",
		Agents: []AgentInput{
			{ID: "agent-1", Branch: "agent/validation", Intent: "input validation"},
			{ID: "agent-2", Branch: "agent/ratelimit", Intent: "rate limiting"},
		},
		Resolver: resolver.UnionResolver{}, Ladder: newLadder(), MaxRetries: 2,
	})
	if err != nil {
		t.Fatal(err)
	}
	if !res.Green {
		t.Fatalf("merge not green: %s", res.Trail.FinalTest)
	}

	// Rung 5: on the merged tree, every agent's established behavior survives.
	violations, err := contract.CheckPreservation(context.Background(), dir, forks, contracts, runner)
	if err != nil {
		t.Fatal(err)
	}
	if len(violations) != 0 {
		t.Fatalf("expected both features to survive, got violations: %+v", violations)
	}
}

// TestMetamorphicCatchesLostFeature proves the rung's teeth: a "pick-winner"
// merge that keeps validation but drops the rate-limit call leaves a CLEAN BUILD
// and a green-looking suite for the tests that remain — yet agent-2's contract
// no longer holds. Rung 5 catches it where build (Rung 2) cannot.
func TestMetamorphicCatchesLostFeature(t *testing.T) {
	dir := initRepo(t)
	commitBaseline(t, dir, map[string]string{
		"go.mod":       "module example.com/app\n\ngo 1.26\n",
		"app.go":       mmBase,
		"base_test.go": mmBaseTest,
	})
	makeBranch(t, dir, "agent/validation", map[string]string{"app.go": mmValidation, "validation_test.go": mmValidationTest})
	makeBranch(t, dir, "agent/ratelimit", map[string]string{"app.go": mmRatelimit, "ratelimit_test.go": mmRatelimitTest})

	contracts := mmContracts()
	runner := contract.GoTestRunner{}
	branches := map[string]string{"agent-1": "agent/validation", "agent-2": "agent/ratelimit"}
	forks := snapshotForks(t, dir, branches, contracts, runner)

	// Simulate a bad (pick-winner) merge: validation survives, rate-limit does
	// not — but BOTH agents' test files are still present, so the build is clean.
	if _, err := gitx.Ok(dir, "checkout", "-q", "-B", "braid/bad-merge", "baseline"); err != nil {
		t.Fatal(err)
	}
	writeFiles(t, dir, map[string]string{
		"app.go":             mmValidation, // only validate(id) in getUser
		"validation_test.go": mmValidationTest,
		"ratelimit_test.go":  mmRatelimitTest, // agent-2's test is kept...
	})
	mustGit(t, dir, "add", "-A")
	mustGit(t, dir, "commit", "-q", "-m", "bad merge: picked validation, dropped rate limiting")

	// Sanity: the tree builds clean (Rung 2 would pass).
	cmd := exec.Command("go", "build", "./...")
	cmd.Dir = dir
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("expected a clean build on the bad merge, got: %s", out)
	}

	// Rung 5 catches the lost feature: agent-2's contract regressed.
	violations, err := contract.CheckPreservation(context.Background(), dir, forks, contracts, runner)
	if err != nil {
		t.Fatal(err)
	}
	if len(violations) != 1 {
		t.Fatalf("expected exactly 1 violation (agent-2's lost feature), got %d: %+v", len(violations), violations)
	}
	v := violations[0]
	if v.Agent != "agent-2" || v.Reason != "regressed" || v.Test != "TestRatelimit" {
		t.Fatalf("unexpected violation: %+v", v)
	}
}

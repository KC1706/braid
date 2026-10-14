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

// phasedRunner passes every check on the fork snapshots (the first len(forks)
// calls, one per contract), then, on the merged-tree preservation pass, fails
// the named agent's check — simulating a feature that did not survive. It lets an
// engine-level test drive the Rung-5 red path without crafting a breaking merge.
type phasedRunner struct {
	contracts int
	calls     int
	failAgent string // which agent's check to fail on the merged pass
}

func (p *phasedRunner) RunCheck(_ context.Context, _ string, c contract.Check) (contract.CheckResult, error) {
	p.calls++
	merged := p.calls > p.contracts // forks are snapshotted first, then the merge
	res := contract.CheckResult{Check: c.Name, Ran: true, Passed: true}
	if merged && c.Test == "TestRatelimit" && p.failAgent == "agent-2" {
		res.Passed = false
		res.Detail = "injected regression"
	}
	return res, nil
}

// TestIntegrateMetamorphicGreenPath wires real contracts through Integrate and
// confirms the engine records a passing Rung-5 (both contracts checked) and stays
// green when every feature survives.
func TestIntegrateMetamorphicGreenPath(t *testing.T) {
	dir := initRepo(t)
	commitBaseline(t, dir, map[string]string{
		"go.mod":       "module example.com/app\n\ngo 1.26\n",
		"app.go":       mmBase,
		"base_test.go": mmBaseTest,
	})
	makeBranch(t, dir, "agent/validation", map[string]string{"app.go": mmValidation, "validation_test.go": mmValidationTest})
	makeBranch(t, dir, "agent/ratelimit", map[string]string{"app.go": mmRatelimit, "ratelimit_test.go": mmRatelimitTest})

	res, err := Integrate(context.Background(), Options{
		RepoDir: dir, Baseline: "baseline", Task: "metamorphic-green",
		Agents: []AgentInput{
			{ID: "agent-1", Branch: "agent/validation", Intent: "input validation"},
			{ID: "agent-2", Branch: "agent/ratelimit", Intent: "rate limiting"},
		},
		Resolver: resolver.UnionResolver{}, Ladder: newLadder(), MaxRetries: 2,
		Contracts: mmContracts(),
	})
	if err != nil {
		t.Fatal(err)
	}
	if !res.Green || res.Verdict != "green" {
		t.Fatalf("want green, got verdict=%q green=%v", res.Verdict, res.Green)
	}
	m := res.Trail.Metamorphic
	if m == nil || !m.Passed || m.Checked != 2 || len(m.Violations) != 0 {
		t.Fatalf("metamorphic record wrong: %+v", m)
	}
}

// TestIntegrateMetamorphicRedPath confirms the engine flips a build-and-test-green
// merge to red when a contract does not survive, and records the violation.
func TestIntegrateMetamorphicRedPath(t *testing.T) {
	dir := initRepo(t)
	commitBaseline(t, dir, map[string]string{
		"go.mod":       "module example.com/app\n\ngo 1.26\n",
		"app.go":       mmBase,
		"base_test.go": mmBaseTest,
	})
	makeBranch(t, dir, "agent/validation", map[string]string{"app.go": mmValidation, "validation_test.go": mmValidationTest})
	makeBranch(t, dir, "agent/ratelimit", map[string]string{"app.go": mmRatelimit, "ratelimit_test.go": mmRatelimitTest})

	runner := &phasedRunner{contracts: 2, failAgent: "agent-2"}
	res, err := Integrate(context.Background(), Options{
		RepoDir: dir, Baseline: "baseline", Task: "metamorphic-red",
		Agents: []AgentInput{
			{ID: "agent-1", Branch: "agent/validation", Intent: "input validation"},
			{ID: "agent-2", Branch: "agent/ratelimit", Intent: "rate limiting"},
		},
		Resolver: resolver.UnionResolver{}, Ladder: newLadder(), MaxRetries: 2,
		Contracts: mmContracts(), MetamorphicRunner: runner,
	})
	if err != nil {
		t.Fatal(err)
	}
	if res.Green || res.Verdict != "red" {
		t.Fatalf("want red from metamorphic, got verdict=%q green=%v", res.Verdict, res.Green)
	}
	m := res.Trail.Metamorphic
	if m == nil || m.Passed || len(m.Violations) != 1 {
		t.Fatalf("metamorphic record wrong: %+v", m)
	}
	if m.Violations[0].Agent != "agent-2" {
		t.Fatalf("violation agent = %q, want agent-2", m.Violations[0].Agent)
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

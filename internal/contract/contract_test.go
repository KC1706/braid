package contract

import (
	"context"
	"reflect"
	"testing"

	"github.com/KC1706/braid/internal/exec"
)

func sampleContract() Contract {
	return Contract{
		Agent:  "agent-1",
		Intent: "validate getUser input",
		Checks: []Check{
			{Name: "rejects-empty-id", Package: "./internal/users", Test: "TestGetUserRejectsEmptyID"},
			{Name: "accepts-valid-id", Package: "./internal/users", Test: "TestGetUserAcceptsValid"},
		},
	}
}

func TestHashOrderIndependentButContentSensitive(t *testing.T) {
	c := sampleContract()
	// Re-ordering the checks must not change the hash.
	reordered := c
	reordered.Checks = []Check{c.Checks[1], c.Checks[0]}
	if c.Hash() != reordered.Hash() {
		t.Fatalf("hash changed under reordering: %s vs %s", c.Hash(), reordered.Hash())
	}
	// Changing WHAT is asserted must change the hash.
	changed := sampleContract()
	changed.Checks[0].Test = "TestGetUserRejectsNil"
	if c.Hash() == changed.Hash() {
		t.Fatal("hash unchanged after changing a check's test name")
	}
	// Changing the intent must change the hash.
	intent := sampleContract()
	intent.Intent = "something else"
	if c.Hash() == intent.Hash() {
		t.Fatal("hash unchanged after changing intent")
	}
}

func TestFreezeVerifyDetectsTamper(t *testing.T) {
	f := Freeze(sampleContract(), "2026-10-14T00:00:00Z")
	if f.SpecHash != sampleContract().Hash() {
		t.Fatal("frozen hash does not match the contract hash")
	}
	if err := f.Verify(); err != nil {
		t.Fatalf("intact contract failed Verify: %v", err)
	}
	// Edit the spec after freezing (bend the spec to the code) -> tamper.
	f.Contract.Checks[0].Test = "TestWhateverThePassingCodeDoes"
	if err := f.Verify(); err != ErrSpecTampered {
		t.Fatalf("expected ErrSpecTampered, got %v", err)
	}
}

func TestMissingTests(t *testing.T) {
	c := sampleContract()
	// Only one of the two named tests exists in source.
	srcs := []string{`
package users
import "testing"
func TestGetUserRejectsEmptyID(t *testing.T) {}
func helper() {}
`}
	missing := c.MissingTests(srcs)
	want := []string{"TestGetUserAcceptsValid"}
	if !reflect.DeepEqual(missing, want) {
		t.Fatalf("missing = %v, want %v", missing, want)
	}
	// With both present, nothing is missing.
	srcs = append(srcs, "package users\nimport \"testing\"\nfunc TestGetUserAcceptsValid(t *testing.T) {}\n")
	if got := c.MissingTests(srcs); len(got) != 0 {
		t.Fatalf("expected no missing tests, got %v", got)
	}
	// A check with an empty Test name imposes no static obligation.
	c.Checks = append(c.Checks, Check{Name: "manual", Test: ""})
	if got := c.MissingTests(srcs); len(got) != 0 {
		t.Fatalf("empty-test check should be skipped, got %v", got)
	}
}

// fakeRunner returns canned results keyed by repoDir + check name, so the
// metamorphic comparison can be tested without a go toolchain.
type fakeRunner struct {
	results map[string]CheckResult // key: repoDir + "|" + check.Name
}

func (f fakeRunner) RunCheck(_ context.Context, repoDir string, c Check) (CheckResult, error) {
	if r, ok := f.results[repoDir+"|"+c.Name]; ok {
		r.Check = c.Name
		return r, nil
	}
	// default: not found on this tree
	return CheckResult{Check: c.Name, Ran: false, Passed: false}, nil
}

func TestCheckPreservationBothFeaturesSurvive(t *testing.T) {
	c1 := Contract{Agent: "agent-1", Checks: []Check{{Name: "validation", Test: "TestValidation"}}}
	c2 := Contract{Agent: "agent-2", Checks: []Check{{Name: "ratelimit", Test: "TestRatelimit"}}}

	// Each feature passes on its own fork.
	forks := []Snapshot{
		{Agent: "agent-1", Results: []CheckResult{{Check: "validation", Ran: true, Passed: true}}},
		{Agent: "agent-2", Results: []CheckResult{{Check: "ratelimit", Ran: true, Passed: true}}},
	}
	// On the merged tree, both still pass.
	runner := fakeRunner{results: map[string]CheckResult{
		"merged|validation": {Ran: true, Passed: true},
		"merged|ratelimit":  {Ran: true, Passed: true},
	}}
	v, err := CheckPreservation(context.Background(), "merged", forks, []Contract{c1, c2}, runner)
	if err != nil {
		t.Fatal(err)
	}
	if len(v) != 0 {
		t.Fatalf("expected no violations, got %v", v)
	}
}

func TestCheckPreservationCatchesDroppedAndRegressed(t *testing.T) {
	c1 := Contract{Agent: "agent-1", Checks: []Check{{Name: "validation", Test: "TestValidation"}}}
	c2 := Contract{Agent: "agent-2", Checks: []Check{{Name: "ratelimit", Test: "TestRatelimit"}}}
	forks := []Snapshot{
		{Agent: "agent-1", Results: []CheckResult{{Check: "validation", Ran: true, Passed: true}}},
		{Agent: "agent-2", Results: []CheckResult{{Check: "ratelimit", Ran: true, Passed: true}}},
	}
	// On the merge: agent-1's test regressed (ran, failed); agent-2's vanished.
	runner := fakeRunner{results: map[string]CheckResult{
		"merged|validation": {Ran: true, Passed: false, Detail: "--- FAIL: TestValidation"},
		// no entry for ratelimit -> did-not-run
	}}
	v, err := CheckPreservation(context.Background(), "merged", forks, []Contract{c1, c2}, runner)
	if err != nil {
		t.Fatal(err)
	}
	if len(v) != 2 {
		t.Fatalf("expected 2 violations, got %d: %v", len(v), v)
	}
	// Sorted by agent: agent-1 regressed first, agent-2 did-not-run.
	if v[0].Agent != "agent-1" || v[0].Reason != "regressed" || v[0].Test != "TestValidation" {
		t.Fatalf("violation[0] wrong: %+v", v[0])
	}
	if v[1].Agent != "agent-2" || v[1].Reason != "did-not-run" || v[1].Test != "TestRatelimit" {
		t.Fatalf("violation[1] wrong: %+v", v[1])
	}
}

func TestCheckPreservationIgnoresForkFailures(t *testing.T) {
	// A check that failed on the fork establishes nothing -> no obligation on merge.
	c := Contract{Agent: "a", Checks: []Check{{Name: "x", Test: "TestX"}}}
	forks := []Snapshot{{Agent: "a", Results: []CheckResult{{Check: "x", Ran: true, Passed: false}}}}
	runner := fakeRunner{results: map[string]CheckResult{}} // x does not run on merge
	v, err := CheckPreservation(context.Background(), "merged", forks, []Contract{c}, runner)
	if err != nil {
		t.Fatal(err)
	}
	if len(v) != 0 {
		t.Fatalf("a fork-failing check should impose no obligation, got %v", v)
	}
}

// cannedExec is a fake exec.Executor that returns a fixed -json stream, to test
// GoTestRunner's stream folding without invoking the go toolchain.
type cannedExec struct {
	stdout string
	stderr string
}

func (c cannedExec) Exec(_ context.Context, _ exec.Request) (exec.Result, error) {
	return exec.Result{Stdout: c.stdout, Stderr: c.stderr}, nil
}

func TestGoTestRunnerParsesPass(t *testing.T) {
	stream := `{"Action":"run","Test":"TestValidation"}
{"Action":"output","Test":"TestValidation","Output":"ok\n"}
{"Action":"pass","Test":"TestValidation"}
`
	r := GoTestRunner{Exec: cannedExec{stdout: stream}}
	res, err := r.RunCheck(context.Background(), "/x", Check{Name: "validation", Test: "TestValidation", Package: "./..."})
	if err != nil {
		t.Fatal(err)
	}
	if !res.Ran || !res.Passed {
		t.Fatalf("expected ran+passed, got %+v", res)
	}
}

func TestGoTestRunnerParsesFail(t *testing.T) {
	stream := `{"Action":"run","Test":"TestValidation"}
{"Action":"output","Test":"TestValidation","Output":"--- FAIL: TestValidation (0.00s)\n"}
{"Action":"fail","Test":"TestValidation"}
`
	r := GoTestRunner{Exec: cannedExec{stdout: stream}}
	res, err := r.RunCheck(context.Background(), "/x", Check{Name: "validation", Test: "TestValidation"})
	if err != nil {
		t.Fatal(err)
	}
	if !res.Ran || res.Passed {
		t.Fatalf("expected ran but not passed, got %+v", res)
	}
	if res.Detail == "" {
		t.Fatal("expected failure detail")
	}
}

func TestGoTestRunnerDidNotRun(t *testing.T) {
	// Empty stream (test name matched nothing) + a stderr build error.
	r := GoTestRunner{Exec: cannedExec{stdout: "", stderr: "users.go:3:1: undefined: Foo"}}
	res, err := r.RunCheck(context.Background(), "/x", Check{Name: "validation", Test: "TestValidation", Package: "./internal/users"})
	if err != nil {
		t.Fatal(err)
	}
	if res.Ran || res.Passed {
		t.Fatalf("expected did-not-run, got %+v", res)
	}
	if res.Detail == "" {
		t.Fatal("expected detail explaining the no-run")
	}
}

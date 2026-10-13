package ladder

import (
	"context"
	"testing"

	"github.com/KC1706/braid/internal/exec"
)

// TestLadderRunsGreenInsideSandbox is the Phase 1 gate core (GO_PRODUCTION_PLAN
// §3): the real build and test rungs, executed inside the sandbox — no
// credentials in the process, network egress denied — still reach a green
// verdict on a clean tree. The same rung code runs; only the Executor changes.
func TestLadderRunsGreenInsideSandbox(t *testing.T) {
	if !(exec.SeatbeltBackend{}).Available() {
		t.Skip("no OS sandbox backend that enforces isolation on this host")
	}
	dir := newFixtureRepo(t)

	sb := exec.SandboxExecutor{} // auto backend + DefaultPolicy (deny egress, GOPROXY=off)
	l := &Ladder{
		Build: GoBuildVerifier{VetToo: true, Exec: sb},
		Test:  GoTestVerifier{Exec: sb},
	}
	res, err := l.Run(context.Background(), dir, "baseline")
	if err != nil {
		t.Fatal(err)
	}
	if res.Verdict != "green" || !res.Passed {
		t.Fatalf("ladder should be green inside the sandbox: verdict=%q passed=%v\n%s",
			res.Verdict, res.Passed, res.Output)
	}
	if len(res.Rungs) != 3 {
		t.Errorf("want guard+build+test = 3 rungs, got %d", len(res.Rungs))
	}
}

// TestSandboxBuildStillCatchesBrokenMerge confirms isolation does not blunt the
// verdict: a clean-but-broken merge still fails the build rung inside the sandbox.
func TestSandboxBuildStillCatchesBrokenMerge(t *testing.T) {
	if !(exec.SeatbeltBackend{}).Available() {
		t.Skip("no OS sandbox backend that enforces isolation on this host")
	}
	dir := newFixtureRepo(t)
	write(t, dir, "caller.go", "package fixture\n\nfunc UseAdd() int { return Addd(1, 2) }\n")

	b, err := GoBuildVerifier{Exec: exec.SandboxExecutor{}}.Verify(context.Background(), dir)
	if err != nil {
		t.Fatal(err)
	}
	if b.Passed {
		t.Fatal("build must still fail on an undefined reference inside the sandbox (C1)")
	}
}

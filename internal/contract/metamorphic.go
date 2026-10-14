package contract

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"strings"

	"github.com/KC1706/braid/internal/exec"
)

// CheckResult is the outcome of running one Check against a working tree.
type CheckResult struct {
	Check  string `json:"check"`  // the Check.Name
	Passed bool   `json:"passed"` // the named test passed on this tree
	Ran    bool   `json:"ran"`    // the named test actually executed (false = not found / build broke)
	Detail string `json:"detail,omitempty"`
}

// Snapshot is an agent's observable behavior on ONE working tree: the result of
// each of its contract checks. Captured on the agent's own fork, it is the
// golden the merged tree must still reproduce (Rung 5).
type Snapshot struct {
	Agent   string        `json:"agent"`
	Results []CheckResult `json:"results"`
}

// result returns the CheckResult for a named check, or ok=false if absent.
func (s Snapshot) result(name string) (CheckResult, bool) {
	for _, r := range s.Results {
		if r.Check == name {
			return r, true
		}
	}
	return CheckResult{}, false
}

// CheckRunner runs one Check against the working tree at repoDir and reports
// whether its named test ran and passed. It is the seam over the go toolchain
// (GoTestRunner) so the metamorphic logic stays unit-testable with a fake.
type CheckRunner interface {
	RunCheck(ctx context.Context, repoDir string, c Check) (CheckResult, error)
}

// TakeSnapshot runs every check in the contract against repoDir (which the caller
// has already checked out to the agent's fork) and records the results. This is
// the "golden" capture of the agent's behavior on its own branch.
func TakeSnapshot(ctx context.Context, repoDir string, c Contract, r CheckRunner) (Snapshot, error) {
	snap := Snapshot{Agent: c.Agent}
	for _, chk := range c.Checks {
		res, err := r.RunCheck(ctx, repoDir, chk)
		if err != nil {
			return Snapshot{}, err
		}
		snap.Results = append(snap.Results, res)
	}
	return snap, nil
}

// Violation is one contract check that passed on an agent's own fork but no
// longer passes on the merged tree: the agent's feature did not survive the
// merge. Reason distinguishes a regressed test from one that vanished (dropped
// test file / renamed test) or whose package stopped building.
type Violation struct {
	Agent  string `json:"agent"`
	Check  string `json:"check"`
	Test   string `json:"test"`
	Reason string `json:"reason"` // "regressed" | "did-not-run"
	Detail string `json:"detail,omitempty"`
}

// diffSnapshots is the pure comparison at the heart of Rung 5: for every check
// that passed on the fork, the merged tree must still run AND pass it. A check
// that failed (or never ran) on the fork imposes no obligation — the metamorphic
// relation preserves only behavior the agent actually established. Output is
// sorted (agent, then check) for determinism.
func diffSnapshots(forks []Snapshot, merged map[string]Snapshot) []Violation {
	var violations []Violation
	for _, fork := range forks {
		m := merged[fork.Agent]
		for _, fr := range fork.Results {
			if !fr.Passed {
				continue // nothing was established, so nothing to preserve
			}
			mr, ok := m.result(fr.Check)
			switch {
			case !ok || !mr.Ran:
				violations = append(violations, Violation{
					Agent: fork.Agent, Check: fr.Check, Reason: "did-not-run",
					Detail: "passed on the fork but did not run on the merged tree (test removed/renamed or package broke)",
				})
			case !mr.Passed:
				violations = append(violations, Violation{
					Agent: fork.Agent, Check: fr.Check, Reason: "regressed",
					Detail: mr.Detail,
				})
			}
		}
	}
	sort.Slice(violations, func(i, j int) bool {
		if violations[i].Agent != violations[j].Agent {
			return violations[i].Agent < violations[j].Agent
		}
		return violations[i].Check < violations[j].Check
	})
	return violations
}

// CheckPreservation is the Rung-5 gate. Given each agent's fork snapshot (the
// goldens) and their contracts, it re-runs every contract on the MERGED tree
// (mergedRepoDir, already checked out to the integration result) and returns the
// violations — checks that passed on a fork but no longer pass on the merge. An
// empty result means every agent's established behavior survived the merge.
func CheckPreservation(ctx context.Context, mergedRepoDir string, forks []Snapshot, contracts []Contract, r CheckRunner) ([]Violation, error) {
	merged := map[string]Snapshot{}
	for _, c := range contracts {
		snap, err := TakeSnapshot(ctx, mergedRepoDir, c, r)
		if err != nil {
			return nil, err
		}
		merged[c.Agent] = snap
	}
	// Fill in Test names from the contracts for richer violation records.
	testByAgentCheck := map[string]string{}
	for _, c := range contracts {
		for _, chk := range c.Checks {
			testByAgentCheck[c.Agent+"\x00"+chk.Name] = chk.Test
		}
	}
	violations := diffSnapshots(forks, merged)
	for i := range violations {
		violations[i].Test = testByAgentCheck[violations[i].Agent+"\x00"+violations[i].Check]
	}
	return violations, nil
}

// GoTestRunner implements CheckRunner over the go toolchain: it runs
// `go test -run ^<Test>$ -count=1 -json <Package>` and folds the JSON stream into
// a single check result. -count=1 defeats the test cache so the metamorphic
// re-run on the merged tree genuinely re-executes. The Exec seam is the same one
// the build/test rungs use, so the metamorphic rung runs inside the sandbox too.
type GoTestRunner struct {
	Exec exec.Executor
}

// goTestEvent is one record of `go test -json` output (subset).
type goTestEvent struct {
	Action string `json:"Action"`
	Test   string `json:"Test"`
	Output string `json:"Output"`
}

func (g GoTestRunner) RunCheck(ctx context.Context, repoDir string, c Check) (CheckResult, error) {
	ex := g.Exec
	if ex == nil {
		ex = exec.LocalExecutor{}
	}
	pkg := c.Package
	if pkg == "" {
		pkg = "./..."
	}
	res, err := ex.Exec(ctx, exec.Request{
		Dir:  repoDir,
		Argv: []string{"go", "test", "-run", "^" + c.Test + "$", "-count=1", "-json", pkg},
		Env:  []string{"CI=true"},
	})
	if err != nil {
		return CheckResult{}, err // misuse only; infra failure surfaces as err
	}

	passed, failed, ran := 0, 0, false
	var detail []string
	for _, line := range strings.Split(res.Stdout, "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		var ev goTestEvent
		if json.Unmarshal([]byte(line), &ev) != nil {
			continue
		}
		if ev.Test != c.Test {
			continue // ignore other tests the pattern might admit
		}
		switch ev.Action {
		case "run":
			ran = true
		case "pass":
			passed++
		case "fail":
			failed++
		case "output":
			if t := strings.TrimRight(ev.Output, "\n"); strings.Contains(t, "FAIL") || strings.Contains(t, "---") {
				detail = append(detail, strings.TrimSpace(t))
			}
		}
	}

	out := CheckResult{Check: c.Name, Ran: ran, Passed: ran && failed == 0 && passed > 0}
	if !out.Passed {
		d := strings.Join(detail, "\n")
		if d == "" {
			if !ran {
				d = fmt.Sprintf("test %s did not run in %s (not found or build error)\n%s", c.Test, pkg, tail(res.Stderr, 20))
			} else {
				d = tail(res.Stderr, 20)
			}
		}
		out.Detail = strings.TrimSpace(d)
	}
	return out, nil
}

func tail(s string, n int) string {
	lines := strings.Split(strings.TrimRight(s, "\n"), "\n")
	if len(lines) > n {
		lines = lines[len(lines)-n:]
	}
	return strings.Join(lines, "\n")
}

// Package ladder is Braid's verification ladder (PRODUCTION_PLAN.md §5.1): it
// replaces an exit-code boolean with a structured, cheapest-first sequence of
// rungs that short-circuits on the first failure. It is the Go port of the TS
// integrator's ladder.ts, adapted for Go targets:
//
//	Rung 1  StructuralGuards  deterministic, pre-execution (tests are immutable inputs)
//	Rung 2  Build/typecheck   `go build ./...` — a dangling cross-package ref is a compile error
//	Rung 3  Semantic          attribute + repair hook (wired by the merge engine; added in §5.4/§5.6)
//	Rung 4  Test execution    `go test -json ./...`
//
// In Go, a textually-clean merge that broke a cross-package reference fails to
// compile, so Rung 2 catches robustly what the TS Rung 3 symbol graph had to
// detect by hand (GO_MIGRATION_PLAN.md §1.2).
package ladder

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"

	"github.com/KC1706/braid/pkg/gitx"
	"github.com/KC1706/braid/pkg/models"
)

// Verifier runs one execution-based rung against the merged working tree. It
// returns a RungResult (Passed=false is a normal red, not an error); a non-nil
// error means the rung could not be evaluated (infra failure → inconclusive).
type Verifier interface {
	Verify(ctx context.Context, repoDir string) (models.RungResult, error)
}

// Ladder sequences the rungs. Build (Rung 2) and Test (Rung 4) are pluggable and
// optional: a nil verifier is skipped, which keeps unit tests focused and leaves
// a clean seam for the semantic rung (Rung 3) the merge engine wires in later.
type Ladder struct {
	TestFilePatterns []*regexp.Regexp
	Build            Verifier
	Test             Verifier
}

// IsTestFile reports whether path is a Go test file (`_test.go`).
func IsTestFile(path string) bool {
	return strings.HasSuffix(path, "_test.go")
}

// Run executes the ladder against the merged working tree at repoDir, comparing
// against baselineRef for the structural guards. Cheapest-first; returns on the
// first failing rung with that rung's verdict.
func (l *Ladder) Run(ctx context.Context, repoDir, baselineRef string) (models.LadderResult, error) {
	var rungs []models.RungResult

	guard, err := StructuralGuards(repoDir, baselineRef)
	if err != nil {
		return models.LadderResult{}, err
	}
	rungs = append(rungs, guard)
	if !guard.Passed {
		return decide(models.VerdictRed, guard, rungs), nil
	}

	if l.Build != nil {
		build, err := l.Build.Verify(ctx, repoDir)
		if err != nil {
			return models.LadderResult{}, err
		}
		rungs = append(rungs, build)
		if !build.Passed {
			return decide(models.VerdictRed, build, rungs), nil
		}
	}

	if l.Test != nil {
		test, err := l.Test.Verify(ctx, repoDir)
		if err != nil {
			return models.LadderResult{}, err
		}
		rungs = append(rungs, test)
		if !test.Passed {
			return decide(models.VerdictRed, test, rungs), nil
		}
	}

	last := rungs[len(rungs)-1]
	return decide(models.VerdictGreen, last, rungs), nil
}

// decide folds the deciding rung into a LadderResult.
func decide(v models.Verdict, deciding models.RungResult, rungs []models.RungResult) models.LadderResult {
	out := deciding.Detail
	if out == "" {
		out = deciding.Summary
	}
	return models.LadderResult{
		Verdict: v,
		Passed:  v == models.VerdictGreen,
		Summary: deciding.Summary,
		Output:  out,
		Rungs:   rungs,
	}
}

var (
	// func Test.../Benchmark.../Fuzz... declarations (one test case each).
	reTestFunc = regexp.MustCompile(`(?m)^func\s+(Test|Benchmark|Fuzz)\w*\s*\(`)
	// t.Run("name", ...) subtests add granularity.
	reSubtest = regexp.MustCompile(`\.Run\s*\(`)
)

// CountTestFuncs counts executable test cases in a Go test source string: each
// top-level func Test/Benchmark/Fuzz plus each t.Run subtest. It is deterministic
// and static (no execution). A `t.Skip()` sits inside a kept func, so — unlike
// the TS `.skip` trick — this static count cannot see skip-based weakening; that
// is a known gap the Keploy coverage deltas close later (GO_MIGRATION_PLAN.md
// §1.4). The regex form is used (rather than go/ast) so the guard still counts a
// tree that no longer parses — a broken merge is the exact case it must judge.
func CountTestFuncs(src string) int {
	// Strip block + line comments so commented-out tests don't inflate the count.
	stripped := stripComments(src)
	return len(reTestFunc.FindAllString(stripped, -1)) + len(reSubtest.FindAllString(stripped, -1))
}

var (
	reBlockComment = regexp.MustCompile(`(?s)/\*.*?\*/`)
	reLineComment  = regexp.MustCompile(`(?m)//.*$`)
)

func stripComments(src string) string {
	src = reBlockComment.ReplaceAllString(src, "")
	src = reLineComment.ReplaceAllString(src, "")
	return src
}

// StructuralGuards is Rung 1: compare the frozen baseline tree against the merged
// working tree and reject the merge if any test file was removed or had its
// executable test-case count reduced (per-file or in total). Mechanical
// enforcement of Decision 2 (tests are immutable merge inputs): fixes C2 ("the
// cheapest way to green is to delete the test"), first line against C14. No tests
// are executed here.
func StructuralGuards(repoDir, baselineRef string) (models.RungResult, error) {
	all, err := gitx.LsFiles(repoDir, baselineRef)
	if err != nil {
		return models.RungResult{}, err
	}
	var baseFiles []string
	for _, f := range all {
		if IsTestFile(f) {
			baseFiles = append(baseFiles, f)
		}
	}
	sort.Strings(baseFiles) // determinism (GO_MIGRATION_PLAN.md §1.5)

	var violations []string
	baseTotal, nowTotal := 0, 0

	for _, file := range baseFiles {
		baseSrc, _, err := gitx.ShowFile(repoDir, baselineRef, file)
		if err != nil {
			return models.RungResult{}, err
		}
		baseCount := CountTestFuncs(baseSrc)
		baseTotal += baseCount

		abs := filepath.Join(repoDir, file)
		nowBytes, readErr := os.ReadFile(abs)
		if readErr != nil {
			violations = append(violations, fmt.Sprintf("test file deleted: %s (had %d test case(s))", file, baseCount))
			continue
		}
		nowCount := CountTestFuncs(string(nowBytes))
		nowTotal += nowCount
		if nowCount < baseCount {
			violations = append(violations, fmt.Sprintf(
				"test coverage dropped in %s: %d → %d executable test case(s)", file, baseCount, nowCount))
		}
	}

	if nowTotal < baseTotal {
		violations = append(violations, fmt.Sprintf(
			"total executable test cases dropped: %d → %d", baseTotal, nowTotal))
	}

	if len(violations) > 0 {
		lines := make([]string, len(violations))
		for i, v := range violations {
			lines[i] = "  - " + v
		}
		return models.RungResult{
			Rung: 1, Name: "structural-guards", Passed: false,
			Summary: fmt.Sprintf("structural guard rejected the merge (%d violation(s))", len(violations)),
			Detail: "Test files are immutable merge inputs; the merge may not remove or weaken them.\n" +
				strings.Join(lines, "\n"),
		}, nil
	}

	return models.RungResult{
		Rung: 1, Name: "structural-guards", Passed: true,
		Summary: fmt.Sprintf("structural guards ok (%d test file(s), %d test case(s) preserved)", len(baseFiles), baseTotal),
	}, nil
}

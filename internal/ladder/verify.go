package ladder

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/KC1706/braid/internal/exec"
	"github.com/KC1706/braid/pkg/models"
)

// GoBuildVerifier is Rung 2: `go build ./...` (and `go vet` when VetToo). In Go a
// textually-clean merge that broke a cross-package reference is a compile error,
// so this rung robustly catches the "clean but broken" merge (C1). Its
// diagnostics are carried as Detail so the semantic rung can attribute the break
// to a renamed/removed symbol.
type GoBuildVerifier struct {
	VetToo bool
	// Exec runs the go subcommands; nil defaults to exec.LocalExecutor (today's
	// host-process behavior). A SandboxExecutor plugs in here unchanged.
	Exec exec.Executor
}

func (v GoBuildVerifier) Verify(ctx context.Context, repoDir string) (models.RungResult, error) {
	ex := executorOr(v.Exec)
	if out, ok := runGo(ctx, ex, repoDir, "build", "./..."); !ok {
		return models.RungResult{
			Rung: 2, Name: "build", Passed: false,
			Summary: "build failed (" + firstLine(out) + ")",
			Detail:  tail(out, 40),
		}, nil
	}
	if v.VetToo {
		if out, ok := runGo(ctx, ex, repoDir, "vet", "./..."); !ok {
			return models.RungResult{
				Rung: 2, Name: "build", Passed: false,
				Summary: "go vet failed (" + firstLine(out) + ")",
				Detail:  tail(out, 40),
			}, nil
		}
	}
	return models.RungResult{
		Rung: 2, Name: "build", Passed: true,
		Summary: "build ok (go build ./... clean)",
	}, nil
}

// GoTestVerifier is Rung 4: the acceptance gate. It runs `go test -json ./...`
// and folds the event stream into pass/fail counts. No model opinion decides
// correctness — only the test outcomes do.
type GoTestVerifier struct {
	// Exec runs `go test`; nil defaults to exec.LocalExecutor. The executor must
	// return stdout and stderr separately (MergeOutput=false) because the -json
	// stream is parsed from stdout and a "could not run" is read from stderr.
	Exec exec.Executor
}

// goTestEvent is one record of `go test -json` output.
type goTestEvent struct {
	Action  string `json:"Action"` // run|pass|fail|skip|output|...
	Package string `json:"Package"`
	Test    string `json:"Test"`
	Output  string `json:"Output"`
}

func (v GoTestVerifier) Verify(ctx context.Context, repoDir string) (models.RungResult, error) {
	// Non-zero exit on a test failure is expected; the verdict comes from the
	// -json stream, so the executor's OK flag is deliberately ignored here.
	res, err := executorOr(v.Exec).Exec(ctx, exec.Request{
		Dir:  repoDir,
		Argv: []string{"go", "test", "-json", "./..."},
		Env:  []string{"CI=true"},
	})
	if err != nil {
		return models.RungResult{}, err // misuse only; infra → inconclusive
	}
	stdout, stderr := res.Stdout, res.Stderr

	passed, failed, skipped := 0, 0, 0
	var failLines []string
	for _, line := range strings.Split(stdout, "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		var ev goTestEvent
		if json.Unmarshal([]byte(line), &ev) != nil {
			continue
		}
		if ev.Test != "" {
			switch ev.Action {
			case "pass":
				passed++
			case "fail":
				failed++
			case "skip":
				skipped++
			}
		}
		if ev.Action == "output" && ev.Test != "" {
			if t := strings.TrimRight(ev.Output, "\n"); strings.Contains(t, "--- FAIL") || strings.Contains(t, "FAIL:") {
				failLines = append(failLines, strings.TrimSpace(t))
			}
		}
	}

	// A build error inside `go test` leaves zero parsed tests and writes to
	// stderr; surface it as red rather than a false green.
	if passed == 0 && failed == 0 && stderr != "" {
		return models.RungResult{
			Rung: 4, Name: "test-execution", Passed: false,
			Summary: "tests could not run (" + firstLine(stderr) + ")",
			Detail:  tail(stderr, 40),
		}, nil
	}

	ok := failed == 0
	summary := fmt.Sprintf("Tests: %d passed, %d failed", passed, failed)
	if skipped > 0 {
		summary += fmt.Sprintf(", %d skipped", skipped)
	}
	rr := models.RungResult{Rung: 4, Name: "test-execution", Passed: ok, Summary: summary}
	if !ok {
		rr.Detail = tail(strings.Join(failLines, "\n")+"\n"+stderr, 40)
	}
	return rr, nil
}

// executorOr returns ex, or the default host-process executor when ex is nil.
func executorOr(ex exec.Executor) exec.Executor {
	if ex == nil {
		return exec.LocalExecutor{}
	}
	return ex
}

// runGo runs a go subcommand in repoDir, returning combined output and whether
// it exited zero. Output is merged (stdout+stderr interleaved) to preserve the
// diagnostic ordering the build rung reports.
func runGo(ctx context.Context, ex exec.Executor, repoDir, sub string, args ...string) (string, bool) {
	res, err := ex.Exec(ctx, exec.Request{
		Dir:         repoDir,
		Argv:        append([]string{"go", sub}, args...),
		MergeOutput: true,
	})
	if err != nil {
		return err.Error(), false
	}
	return res.Combined, res.OK
}

func firstLine(s string) string {
	s = strings.TrimSpace(s)
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		return s[:i]
	}
	if s == "" {
		return "no output"
	}
	return s
}

func tail(s string, n int) string {
	lines := strings.Split(strings.TrimRight(s, "\n"), "\n")
	if len(lines) > n {
		lines = lines[len(lines)-n:]
	}
	return strings.Join(lines, "\n")
}

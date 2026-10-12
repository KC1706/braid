package ladder

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os/exec"
	"strings"

	"github.com/KC1706/braid/pkg/models"
)

// GoBuildVerifier is Rung 2: `go build ./...` (and `go vet` when VetToo). In Go a
// textually-clean merge that broke a cross-package reference is a compile error,
// so this rung robustly catches the "clean but broken" merge (C1). Its
// diagnostics are carried as Detail so the semantic rung can attribute the break
// to a renamed/removed symbol.
type GoBuildVerifier struct {
	VetToo bool
}

func (v GoBuildVerifier) Verify(ctx context.Context, repoDir string) (models.RungResult, error) {
	if out, ok := runGo(ctx, repoDir, "build", "./..."); !ok {
		return models.RungResult{
			Rung: 2, Name: "build", Passed: false,
			Summary: "build failed (" + firstLine(out) + ")",
			Detail:  tail(out, 40),
		}, nil
	}
	if v.VetToo {
		if out, ok := runGo(ctx, repoDir, "vet", "./..."); !ok {
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
type GoTestVerifier struct{}

// goTestEvent is one record of `go test -json` output.
type goTestEvent struct {
	Action  string `json:"Action"` // run|pass|fail|skip|output|...
	Package string `json:"Package"`
	Test    string `json:"Test"`
	Output  string `json:"Output"`
}

func (GoTestVerifier) Verify(ctx context.Context, repoDir string) (models.RungResult, error) {
	cmd := exec.CommandContext(ctx, "go", "test", "-json", "./...")
	cmd.Dir = repoDir
	cmd.Env = append(cmd.Environ(), "CI=true")
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	_ = cmd.Run() // non-zero on test failure is expected; verdict comes from the stream

	passed, failed, skipped := 0, 0, 0
	var failLines []string
	for _, line := range strings.Split(stdout.String(), "\n") {
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
	if passed == 0 && failed == 0 && stderr.Len() > 0 {
		return models.RungResult{
			Rung: 4, Name: "test-execution", Passed: false,
			Summary: "tests could not run (" + firstLine(stderr.String()) + ")",
			Detail:  tail(stderr.String(), 40),
		}, nil
	}

	ok := failed == 0
	summary := fmt.Sprintf("Tests: %d passed, %d failed", passed, failed)
	if skipped > 0 {
		summary += fmt.Sprintf(", %d skipped", skipped)
	}
	res := models.RungResult{Rung: 4, Name: "test-execution", Passed: ok, Summary: summary}
	if !ok {
		res.Detail = tail(strings.Join(failLines, "\n")+"\n"+stderr.String(), 40)
	}
	return res, nil
}

// runGo runs a go subcommand in repoDir, returning combined output and whether
// it exited zero.
func runGo(ctx context.Context, repoDir, sub string, args ...string) (string, bool) {
	cmd := exec.CommandContext(ctx, "go", append([]string{sub}, args...)...)
	cmd.Dir = repoDir
	var out bytes.Buffer
	cmd.Stdout = &out
	cmd.Stderr = &out
	err := cmd.Run()
	return out.String(), err == nil
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

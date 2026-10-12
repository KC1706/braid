// Package gitx is Braid's thin, non-throwing wrapper around the git CLI.
//
// It is the Go port of the TypeScript integrator's git.ts. git uses exit codes
// as signal (e.g. `git merge` exits 1 on conflicts), so Run never treats a
// non-zero exit as a fatal error — callers inspect Result.Code. Ok is the
// assert-success variant for commands that must not fail.
package gitx

import (
	"errors"
	"fmt"
	"os/exec"
	"strings"
)

// Result is the outcome of one git invocation. Code is the process exit code
// (0 on success); Run populates Stdout/Stderr regardless of Code.
type Result struct {
	Stdout string
	Stderr string
	Code   int
}

// Run executes `git <args...>` in cwd and never returns an error for a non-zero
// exit — the exit code is reported in Result.Code so the merge engine can branch
// on it (a conflicted merge is code 1, not a crash).
func Run(cwd string, args ...string) Result {
	cmd := exec.Command("git", args...)
	cmd.Dir = cwd
	var out, errb strings.Builder
	cmd.Stdout = &out
	cmd.Stderr = &errb
	err := cmd.Run()
	code := 0
	if err != nil {
		var ee *exec.ExitError
		if errors.As(err, &ee) {
			code = ee.ExitCode()
		} else {
			// git not found / could not start: surface as a non-zero code with
			// the reason in Stderr rather than panicking.
			code = -1
			if errb.Len() == 0 {
				errb.WriteString(err.Error())
			}
		}
	}
	return Result{Stdout: out.String(), Stderr: errb.String(), Code: code}
}

// Ok runs a git command that must succeed, returning its stdout or an error with
// context. The Go equivalent of git.ts's gitOk.
func Ok(cwd string, args ...string) (string, error) {
	r := Run(cwd, args...)
	if r.Code != 0 {
		detail := r.Stderr
		if detail == "" {
			detail = r.Stdout
		}
		return "", fmt.Errorf("git %s failed (code %d): %s", strings.Join(args, " "), r.Code, detail)
	}
	return r.Stdout, nil
}

// lines splits git output into trimmed, non-empty lines.
func lines(s string) []string {
	out := []string{}
	for _, l := range strings.Split(s, "\n") {
		if t := strings.TrimSpace(l); t != "" {
			out = append(out, t)
		}
	}
	return out
}

// CurrentSHA returns the commit sha at HEAD.
func CurrentSHA(cwd string) (string, error) {
	out, err := Ok(cwd, "rev-parse", "HEAD")
	return strings.TrimSpace(out), err
}

// ConflictedFiles lists files left unmerged after a merge attempt.
func ConflictedFiles(cwd string) ([]string, error) {
	out, err := Ok(cwd, "diff", "--name-only", "--diff-filter=U")
	if err != nil {
		return nil, err
	}
	return lines(out), nil
}

// ChangedFiles lists files that differ between two refs.
func ChangedFiles(cwd, from, to string) ([]string, error) {
	out, err := Ok(cwd, "diff", "--name-only", from, to)
	if err != nil {
		return nil, err
	}
	return lines(out), nil
}

// LsFiles lists every tracked path at a ref (recursive).
func LsFiles(cwd, ref string) ([]string, error) {
	out, err := Ok(cwd, "ls-tree", "-r", "--name-only", ref)
	if err != nil {
		return nil, err
	}
	return lines(out), nil
}

// ShowFile returns the contents of one path at a ref. exists is false (with a
// nil error) when the path does not exist at that ref — the common, non-error
// case the structural guard and semantic repair rely on.
func ShowFile(cwd, ref, path string) (content string, exists bool, err error) {
	r := Run(cwd, "show", ref+":"+path)
	if r.Code == 0 {
		return r.Stdout, true, nil
	}
	// A missing path is the expected "not there" signal, not a failure.
	return "", false, nil
}

// Package exec is Braid's command-execution seam: the one place the verification
// ladder (and, later, agent and resolver steps) shells out to run untrusted or
// semi-trusted work. Today the only implementation is LocalExecutor, which runs
// the command in the host process exactly as the ladder used to — the seam is a
// pure refactor with no behavior change. The point of the seam is the next
// implementation: a SandboxExecutor that runs the same command with no
// credentials in its environment, default-deny egress, and hard resource caps
// (GO_PRODUCTION_PLAN.md Phase 1). Verifiers depend on this interface, never on
// os/exec, so swapping in the sandbox requires no change to a rung.
package exec

import (
	"bytes"
	"context"
	"errors"
	osexec "os/exec"
	"time"
)

// Limits bounds one execution. LocalExecutor enforces only Wall (via the
// context); the sandbox implementation enforces the rest. Zero means "no limit
// from this field", so the zero Limits reproduces today's unbounded local run.
type Limits struct {
	CPU  time.Duration // CPU time (sandbox only)
	Mem  int64         // bytes (sandbox only)
	Pids int           // max processes (sandbox only)
	Wall time.Duration // wall-clock; also honored locally via the context
	Disk int64         // bytes of writable scratch (sandbox only)
}

// Request is one command to run. Argv[0] is the program, resolved on PATH. Env
// entries (e.g. "CI=true") are appended to the inherited process environment. A
// sandbox executor drops credentials and applies EgressPolicy; LocalExecutor
// ignores EgressPolicy.
type Request struct {
	Dir          string
	Argv         []string
	Env          []string
	MergeOutput  bool   // interleave stdout+stderr into Result.Combined
	EgressPolicy string // sandbox only; "" = default (deny for sandbox)
	Limits       Limits
}

// Result is the outcome of one command. A non-zero exit is a normal result
// (OK=false), not an error: the caller decides what a failing build or test
// means, and ExitCode == -1 marks a command that could not run at all. When
// MergeOutput was set, the interleaved output is in Combined and Stdout/Stderr
// are empty; otherwise they are captured separately — the `go test -json` rung
// needs them split (GO_PRODUCTION_PLAN.md §0).
type Result struct {
	Stdout   string
	Stderr   string
	Combined string
	ExitCode int
	OK       bool // ExitCode == 0
}

// Executor runs a command and returns its result. A non-nil error is reserved
// for misuse (an empty Argv); a command that ran — whether it exited 0 or not,
// or failed to start — returns (Result, nil), matching the ladder's original
// os/exec behavior. (The sandbox executor may additionally surface infra
// failures as errors for the ladder's inconclusive path.)
type Executor interface {
	Exec(ctx context.Context, req Request) (Result, error)
}

// LocalExecutor runs the command in the host process via os/exec — the exact
// behavior the ladder had before the seam existed. It inherits the host's
// credentials and network, so it is for local/dev and tests, never for
// untrusted agent or resolver code in production.
type LocalExecutor struct{}

// Exec runs req in the host process. A non-zero exit, or a failure to start the
// process, is folded into Result (OK=false); only an empty Argv is an error.
func (LocalExecutor) Exec(ctx context.Context, req Request) (Result, error) {
	if len(req.Argv) == 0 {
		return Result{}, errors.New("exec: empty Argv")
	}
	if req.Limits.Wall > 0 {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, req.Limits.Wall)
		defer cancel()
	}

	cmd := osexec.CommandContext(ctx, req.Argv[0], req.Argv[1:]...)
	cmd.Dir = req.Dir
	if len(req.Env) > 0 {
		cmd.Env = append(cmd.Environ(), req.Env...)
	}

	var res Result
	var runErr error
	if req.MergeOutput {
		var out bytes.Buffer
		cmd.Stdout = &out
		cmd.Stderr = &out
		runErr = cmd.Run()
		res.Combined = out.String()
	} else {
		var stdout, stderr bytes.Buffer
		cmd.Stdout = &stdout
		cmd.Stderr = &stderr
		runErr = cmd.Run()
		res.Stdout = stdout.String()
		res.Stderr = stderr.String()
	}

	if runErr == nil {
		res.OK = true
		res.ExitCode = 0
		return res, nil
	}
	var ee *osexec.ExitError
	if errors.As(runErr, &ee) {
		res.ExitCode = ee.ExitCode() // ran, exited non-zero
	} else {
		res.ExitCode = -1 // could not start, or was killed by the context
	}
	return res, nil
}

var _ Executor = LocalExecutor{}

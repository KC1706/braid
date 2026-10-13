package exec

import (
	"context"
	"runtime"
	"strings"
	"testing"
	"time"
)

func TestLocalExecutorSeparateStreams(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("uses a POSIX shell")
	}
	res, err := LocalExecutor{}.Exec(context.Background(), Request{
		Argv: []string{"sh", "-c", "echo out; echo err 1>&2"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if !res.OK || res.ExitCode != 0 {
		t.Fatalf("want OK exit 0, got OK=%v code=%d", res.OK, res.ExitCode)
	}
	if strings.TrimSpace(res.Stdout) != "out" {
		t.Errorf("stdout = %q, want %q", res.Stdout, "out")
	}
	if strings.TrimSpace(res.Stderr) != "err" {
		t.Errorf("stderr = %q, want %q", res.Stderr, "err")
	}
	if res.Combined != "" {
		t.Errorf("combined should be empty when MergeOutput is false, got %q", res.Combined)
	}
}

func TestLocalExecutorMergeOutput(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("uses a POSIX shell")
	}
	res, err := LocalExecutor{}.Exec(context.Background(), Request{
		Argv:        []string{"sh", "-c", "echo out; echo err 1>&2"},
		MergeOutput: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	if res.Stdout != "" || res.Stderr != "" {
		t.Errorf("stdout/stderr should be empty when merged, got %q / %q", res.Stdout, res.Stderr)
	}
	if !strings.Contains(res.Combined, "out") || !strings.Contains(res.Combined, "err") {
		t.Errorf("combined should carry both streams, got %q", res.Combined)
	}
}

func TestLocalExecutorNonZeroExitIsNotAnError(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("uses a POSIX shell")
	}
	res, err := LocalExecutor{}.Exec(context.Background(), Request{
		Argv: []string{"sh", "-c", "exit 7"},
	})
	if err != nil {
		t.Fatalf("a non-zero exit must not be a Go error: %v", err)
	}
	if res.OK {
		t.Error("OK should be false on a non-zero exit")
	}
	if res.ExitCode != 7 {
		t.Errorf("exit code = %d, want 7", res.ExitCode)
	}
}

func TestLocalExecutorEnvIsAppended(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("uses a POSIX shell")
	}
	res, err := LocalExecutor{}.Exec(context.Background(), Request{
		Argv: []string{"sh", "-c", "echo $BRAID_TEST_VAR"},
		Env:  []string{"BRAID_TEST_VAR=hello"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if strings.TrimSpace(res.Stdout) != "hello" {
		t.Errorf("env var not visible to the process: stdout = %q", res.Stdout)
	}
}

func TestLocalExecutorStartFailureIsFoldedIntoResult(t *testing.T) {
	// A program that does not exist: the old ladder treated this as ok=false,
	// not as an infra error, so the seam must too (ExitCode -1).
	res, err := LocalExecutor{}.Exec(context.Background(), Request{
		Argv: []string{"braid-no-such-program-xyz"},
	})
	if err != nil {
		t.Fatalf("a start failure must be folded into Result, not returned as err: %v", err)
	}
	if res.OK || res.ExitCode != -1 {
		t.Errorf("want OK=false exit=-1 for a missing program, got OK=%v code=%d", res.OK, res.ExitCode)
	}
}

func TestLocalExecutorEmptyArgvIsMisuse(t *testing.T) {
	if _, err := (LocalExecutor{}).Exec(context.Background(), Request{}); err == nil {
		t.Error("empty Argv should return an error")
	}
}

func TestLocalExecutorWallLimitCancels(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("uses a POSIX shell")
	}
	start := time.Now()
	res, err := LocalExecutor{}.Exec(context.Background(), Request{
		Argv:   []string{"sh", "-c", "sleep 10"},
		Limits: Limits{Wall: 100 * time.Millisecond},
	})
	if err != nil {
		t.Fatal(err)
	}
	if res.OK {
		t.Error("a command killed by the wall limit should not be OK")
	}
	if elapsed := time.Since(start); elapsed > 3*time.Second {
		t.Errorf("wall limit did not cut the command short: ran %s", elapsed)
	}
}

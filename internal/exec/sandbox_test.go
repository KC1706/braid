package exec

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	osexec "os/exec"
	"runtime"
	"strings"
	"testing"
)

func TestSanitizeEnvDropsNonAllowlisted(t *testing.T) {
	t.Setenv("BRAID_FAKE_TOKEN", "art_v2_x_deadbeef")
	t.Setenv("PATH", "/usr/bin:/bin")

	env := SanitizeEnv([]string{"PATH"}, []string{"GOPROXY=off"})

	var sawPath, sawExtra bool
	for _, kv := range env {
		if strings.HasPrefix(kv, "BRAID_FAKE_TOKEN=") {
			t.Errorf("a non-allowlisted secret leaked through: %q", kv)
		}
		if kv == "PATH=/usr/bin:/bin" {
			sawPath = true
		}
		if kv == "GOPROXY=off" {
			sawExtra = true
		}
	}
	if !sawPath {
		t.Error("allowlisted PATH should pass through")
	}
	if !sawExtra {
		t.Error("extra env should be appended")
	}
}

func TestSanitizeEnvEmptyAllowlistKeepsNothing(t *testing.T) {
	t.Setenv("BRAID_FAKE_TOKEN", "secret")
	env := SanitizeEnv(nil, nil)
	for _, kv := range env {
		if strings.HasPrefix(kv, "BRAID_FAKE_TOKEN=") {
			t.Fatalf("empty allowlist must drop everything, got %q", kv)
		}
	}
}

// The core Phase 1 gate property #1: a credential in the host environment must
// not reach the command the sandbox runs. Uses DirectBackend to test the env
// strip in isolation from any OS backend.
func TestSandboxExecutorStripsCredentialsFromEnv(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("uses a POSIX shell")
	}
	t.Setenv("BRAID_FAKE_TOKEN", "art_v2_x_should_not_appear")

	s := SandboxExecutor{Backend: DirectBackend{}}
	res, err := s.Exec(context.Background(), Request{
		Argv: []string{"sh", "-c", "echo token=[$BRAID_FAKE_TOKEN]; echo path=[$PATH]"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(res.Stdout, "should_not_appear") {
		t.Errorf("the token leaked into the sandboxed process: %q", res.Stdout)
	}
	if !strings.Contains(res.Stdout, "token=[]") {
		t.Errorf("token should be empty inside the sandbox, got %q", res.Stdout)
	}
	if strings.Contains(res.Stdout, "path=[]") {
		t.Errorf("PATH (allowlisted) should survive, got %q", res.Stdout)
	}
}

func TestSeatbeltAvailabilityMatchesHost(t *testing.T) {
	want := runtime.GOOS == "darwin"
	if want {
		if _, err := osexec.LookPath("sandbox-exec"); err != nil {
			want = false
		}
	}
	if got := (SeatbeltBackend{}).Available(); got != want {
		t.Errorf("SeatbeltBackend.Available() = %v, want %v on %s", got, want, runtime.GOOS)
	}
}

func TestAutoBackendPrefersSeatbeltOnDarwin(t *testing.T) {
	b := autoBackend()
	if (SeatbeltBackend{}).Available() {
		if b.Name() != "seatbelt" {
			t.Errorf("auto backend = %q, want seatbelt where available", b.Name())
		}
	} else if b.Name() != "direct" {
		t.Errorf("auto backend = %q, want direct fallback", b.Name())
	}
}

// The core Phase 1 gate property #2: under the sandbox, network egress is
// denied. Verified for real on macOS via Seatbelt; skipped elsewhere until the
// container backend lands.
func TestSandboxDeniesEgress(t *testing.T) {
	if !(SeatbeltBackend{}).Available() {
		t.Skip("no OS sandbox backend that enforces egress on this host")
	}
	if _, err := osexec.LookPath("curl"); err != nil {
		t.Skip("curl not available to probe egress")
	}
	// Sanity: unsandboxed, this host can reach the network (otherwise the denial
	// below would be vacuous).
	base, err := LocalExecutor{}.Exec(context.Background(), Request{
		Argv: []string{"curl", "-sS", "-m", "6", "-o", "/dev/null", "https://example.com"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if !base.OK {
		t.Skip("host has no egress; egress-denial test would be vacuous")
	}

	sandboxed, err := SandboxExecutor{Backend: SeatbeltBackend{}}.Exec(context.Background(), Request{
		Argv:   []string{"curl", "-sS", "-m", "6", "-o", "/dev/null", "https://example.com"},
		Limits: Limits{}, // policy default applies
	})
	if err != nil {
		t.Fatal(err)
	}
	if sandboxed.OK {
		t.Error("network egress should be denied inside the sandbox")
	}
}

// Loopback must stay reachable under the sandbox — a test suite's own
// httptest/localhost servers are not egress, and blocking them would make the
// sandbox unusable for the exact workloads Braid verifies.
func TestSandboxAllowsLoopback(t *testing.T) {
	if !(SeatbeltBackend{}).Available() {
		t.Skip("no OS sandbox backend on this host")
	}
	if _, err := osexec.LookPath("curl"); err != nil {
		t.Skip("curl not available to probe loopback")
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	}))
	defer srv.Close()

	res, err := SandboxExecutor{Backend: SeatbeltBackend{}}.Exec(context.Background(), Request{
		Argv: []string{"curl", "-sS", "-m", "6", "-o", "/dev/null", srv.URL},
	})
	if err != nil {
		t.Fatal(err)
	}
	if !res.OK {
		t.Errorf("loopback should be reachable inside the sandbox: exit=%d stderr=%q", res.ExitCode, res.Stderr)
	}
}

func TestSandboxEgressDenialDoesNotBreakLocalWork(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("uses a POSIX shell")
	}
	// A purely local command must still succeed under the sandbox.
	res, err := SandboxExecutor{}.Exec(context.Background(), Request{
		Argv: []string{"sh", "-c", "echo hello"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if !res.OK || !strings.Contains(res.Stdout, "hello") {
		t.Errorf("local work should run under the sandbox: OK=%v out=%q", res.OK, res.Stdout)
	}
}

func TestMain(m *testing.M) {
	// Keep a clean baseline for env tests regardless of invocation environment.
	os.Exit(m.Run())
}

package exec

import (
	"context"
	"fmt"
	"os"
	osexec "os/exec"
	"runtime"
	"strings"
	"time"
)

// Policy is the security posture the SandboxExecutor applies to every command.
// It encodes the two-zone invariant (GO_PRODUCTION_PLAN.md §1): untrusted code
// runs with no credentials in its environment and no network, under hard caps.
type Policy struct {
	// EnvAllowlist is the set of environment variable NAMES that may pass through
	// from the host. Everything else — API keys, Artifacts tokens — is dropped.
	EnvAllowlist []string
	// ExtraEnv is appended after the allowlisted host vars (e.g. "GOPROXY=off" to
	// keep an offline build from reaching for a module proxy under DenyEgress).
	ExtraEnv []string
	// DenyEgress blocks all network access when a backend can enforce it.
	DenyEgress bool
	// Limits caps CPU/mem/pids/disk/wall for the job.
	Limits Limits
}

// DefaultPolicy is the production-shaped default: a minimal, credential-free
// environment (the vars a toolchain legitimately needs — none of them secret),
// network denied, offline builds, and a 5-minute wall cap. GOCACHE/GOMODCACHE/
// GOPATH pass through when the host set them so an offline build reuses caches.
func DefaultPolicy() Policy {
	return Policy{
		EnvAllowlist: []string{
			"HOME", "PATH", "TMPDIR", "USER", "SHELL",
			"LANG", "LC_ALL", "LC_CTYPE",
			"GOPATH", "GOCACHE", "GOMODCACHE", "GOFLAGS",
		},
		ExtraEnv:   []string{"GOPROXY=off"},
		DenyEgress: true,
		Limits:     Limits{Wall: 5 * time.Minute},
	}
}

// SanitizeEnv builds an exact process environment from the host environment
// filtered to allowlist, then appends extra. The result never contains a
// variable whose name is not on the allowlist, so a credential in the host
// environment cannot reach the command.
func SanitizeEnv(allowlist, extra []string) []string {
	allow := make(map[string]bool, len(allowlist))
	for _, k := range allowlist {
		allow[k] = true
	}
	// Non-nil even when empty: an empty allowlist must mean "no inherited env",
	// not "inherit everything" (which a nil Cmd.Env would do).
	out := make([]string, 0, len(allowlist)+len(extra))
	for _, kv := range os.Environ() {
		if i := strings.IndexByte(kv, '='); i >= 0 && allow[kv[:i]] {
			out = append(out, kv)
		}
	}
	return append(out, extra...)
}

// SandboxBackend applies OS-level isolation to a command by rewrapping the
// Request (e.g. prefixing Argv with `sandbox-exec` or `docker run`). The
// SandboxExecutor has already stripped the environment before Wrap is called;
// the backend adds network/filesystem/resource isolation on top.
type SandboxBackend interface {
	Wrap(req Request, pol Policy) (Request, error)
	Name() string
	Available() bool
}

// SandboxExecutor runs commands under a SandboxBackend with a credential-free
// environment. It composes an inner Executor (LocalExecutor by default), so the
// isolation is layered on top of the same run path the ladder already uses —
// the rung code is identical whether it runs locally or sandboxed.
type SandboxExecutor struct {
	Policy  Policy         // zero value → DefaultPolicy()
	Backend SandboxBackend // nil → autoBackend()
	Inner   Executor       // nil → LocalExecutor
}

// Exec strips the environment to the policy allowlist, applies the policy's
// limits, wraps the command in the backend's isolation, and runs it on the
// inner executor.
func (s SandboxExecutor) Exec(ctx context.Context, req Request) (Result, error) {
	pol := s.Policy
	if pol.EnvAllowlist == nil {
		pol = DefaultPolicy()
	}
	backend := s.Backend
	if backend == nil {
		backend = autoBackend()
	}
	inner := s.Inner
	if inner == nil {
		inner = LocalExecutor{}
	}

	// Credential strip: an exact, allowlisted env including the caller's Env and
	// the policy's ExtraEnv. No host variable outside the allowlist survives.
	extra := append(append([]string{}, pol.ExtraEnv...), req.Env...)
	req.EnvOverride = SanitizeEnv(pol.EnvAllowlist, extra)
	req.Env = nil

	if req.Limits == (Limits{}) {
		req.Limits = pol.Limits
	}

	wrapped, err := backend.Wrap(req, pol)
	if err != nil {
		return Result{}, err
	}
	return inner.Exec(ctx, wrapped)
}

var _ Executor = SandboxExecutor{}

// autoBackend picks the strongest isolation available on this host: Seatbelt on
// macOS, else the no-OS-isolation DirectBackend (env strip only). The container
// and Cloudflare-Sandbox backends plug in here for Linux/prod.
func autoBackend() SandboxBackend {
	if sb := (SeatbeltBackend{}); sb.Available() {
		return sb
	}
	return DirectBackend{}
}

// DirectBackend adds no OS-level isolation — it runs the command as-is. The
// SandboxExecutor's environment strip still applies, so credentials are gone,
// but network and filesystem are NOT confined. It is the honest fallback when no
// real sandbox is available; it must not be the production backend.
type DirectBackend struct{}

func (DirectBackend) Wrap(req Request, _ Policy) (Request, error) { return req, nil }
func (DirectBackend) Name() string                               { return "direct" }
func (DirectBackend) Available() bool                            { return true }

// SeatbeltBackend isolates the command with macOS `sandbox-exec` (Seatbelt). It
// denies all network when Policy.DenyEgress is set — proven to block egress on
// the dev box. Filesystem confinement and resource caps are left to the
// container/Cloudflare backends (they need cgroups); Seatbelt here provides the
// egress-deny half of the two-zone guarantee for local development.
type SeatbeltBackend struct{}

func (SeatbeltBackend) Name() string { return "seatbelt" }

func (SeatbeltBackend) Available() bool {
	if runtime.GOOS != "darwin" {
		return false
	}
	_, err := osexec.LookPath("sandbox-exec")
	return err == nil
}

func (b SeatbeltBackend) Wrap(req Request, pol Policy) (Request, error) {
	if !b.Available() {
		return Request{}, fmt.Errorf("exec: seatbelt backend unavailable on %s", runtime.GOOS)
	}
	if !pol.DenyEgress {
		return req, nil // nothing to enforce; run unwrapped
	}
	// Allow everything by default, then deny all network. Filesystem stays open
	// (confinement is the container backend's job), which keeps toolchain caches
	// and temp dirs working while egress is cut.
	const profile = "(version 1)(allow default)(deny network*)"
	req.Argv = append([]string{"sandbox-exec", "-p", profile}, req.Argv...)
	return req, nil
}

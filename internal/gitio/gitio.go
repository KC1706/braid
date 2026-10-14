package gitio

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"time"
)

// Bundle is the content-addressed carrier the compute zone hands to the I/O zone.
// Path is a git bundle file holding the merged commit and its ancestry; MergedSHA
// is the commit to publish; BaseSHA is the base tip the merge descended from (the
// compare-and-swap expectation); Ref is the branch name the bundle was written
// from, used to fetch it back out. A bundle is self-contained: it needs no
// prerequisite objects on the receiving side.
type Bundle struct {
	Path      string
	MergedSHA string
	BaseSHA   string
	Ref       string
}

// WriteBundle creates a self-contained bundle of mergedRef's full history in the
// COMPUTE repo (repoDir) and returns the Bundle descriptor. baseRef is recorded
// as the CAS expectation. This is the compute zone's only job at the boundary;
// the trusted zone never touches repoDir.
func WriteBundle(ctx context.Context, repoDir, baseRef, mergedRef, bundlePath string) (Bundle, error) {
	baseSHA, err := revParse(ctx, repoDir, baseRef)
	if err != nil {
		return Bundle{}, err
	}
	mergedSHA, err := revParse(ctx, repoDir, mergedRef)
	if err != nil {
		return Bundle{}, err
	}
	// Bundle the full history reachable from mergedRef so the receiver needs no
	// prerequisites; the ref is recorded inside the bundle for fetch-back.
	if _, err := git(ctx, repoDir, "bundle", "create", bundlePath, mergedRef); err != nil {
		return Bundle{}, err
	}
	return Bundle{Path: bundlePath, MergedSHA: mergedSHA, BaseSHA: baseSHA, Ref: mergedRef}, nil
}

// ErrBaseMoved is returned by PushBundle when the remote branch no longer points
// at the bundle's recorded BaseSHA — a concurrent run advanced the base, so this
// merge must be rebased/retried (the Base-Coordinator DO drives that in Phase 4).
var ErrBaseMoved = errors.New("gitio: base tip moved since the merge was computed (CAS failed)")

// Zone is the trusted git-I/O operator: it clones the base and pushes a bundle to
// one remote, holding the scoped Token. It runs git with a scrubbed environment
// and never executes anything but git.
type Zone struct {
	Remote string // remote URL (https Artifacts URL in prod; a path/file:// in tests)
	Token  Token  // scoped, short-lived; zero value = no auth (local remote)
}

// CloneBase clones the remote's branch into workdir (which must be empty) and
// returns the base tip SHA. workdir becomes the trusted working copy the push
// is staged in — it only ever holds base + the imported bundle, never test runs.
func (z Zone) CloneBase(ctx context.Context, workdir, branch string) (baseSHA string, err error) {
	if z.Token.Expired(nowFunc()) {
		return "", fmt.Errorf("gitio: token expired (%s)", z.Token)
	}
	args := append(z.Token.configArgs(), "clone", "--branch", branch, "--single-branch", z.Remote, workdir)
	if _, err := git(ctx, "", args...); err != nil {
		return "", err
	}
	return revParse(ctx, workdir, "HEAD")
}

// PushBundle imports the bundle into workdir (a clone produced by CloneBase) and
// publishes MergedSHA to branch on the remote, enforcing a compare-and-swap: the
// push is a `--force-with-lease` pinned to the bundle's BaseSHA, so if the remote
// branch advanced since the clone the push is rejected with ErrBaseMoved. It
// first verifies the merged commit genuinely descends from BaseSHA, so a bundle
// can never rewrite unrelated history onto the branch.
func (z Zone) PushBundle(ctx context.Context, workdir, branch string, b Bundle) (pushedSHA string, err error) {
	if z.Token.Expired(nowFunc()) {
		return "", fmt.Errorf("gitio: token expired (%s)", z.Token)
	}
	// Local CAS precheck against the freshly cloned base.
	localBase, err := revParse(ctx, workdir, "HEAD")
	if err != nil {
		return "", err
	}
	if localBase != b.BaseSHA {
		return "", ErrBaseMoved
	}
	// Import the merged commit from the content-addressed bundle.
	if _, err := git(ctx, workdir, "fetch", b.Path, b.Ref); err != nil {
		return "", fmt.Errorf("gitio: import bundle: %w", err)
	}
	if got, err := revParse(ctx, workdir, "FETCH_HEAD"); err != nil {
		return "", err
	} else if got != b.MergedSHA {
		return "", fmt.Errorf("gitio: bundle tip %s != declared merged %s", short(got), short(b.MergedSHA))
	}
	// The merge must descend from the recorded base — never graft unrelated work.
	if _, err := git(ctx, workdir, "merge-base", "--is-ancestor", b.BaseSHA, b.MergedSHA); err != nil {
		return "", fmt.Errorf("gitio: merged commit does not descend from base %s", short(b.BaseSHA))
	}
	// Publish with a lease pinned to the base tip = atomic compare-and-swap.
	lease := fmt.Sprintf("--force-with-lease=%s:%s", branch, b.BaseSHA)
	refspec := fmt.Sprintf("%s:%s", b.MergedSHA, branch)
	args := append(z.Token.configArgs(), "push", lease, "origin", refspec)
	out, err := git(ctx, workdir, args...)
	if err != nil {
		if isLeaseFailure(out, err) {
			return "", ErrBaseMoved
		}
		return "", err
	}
	return b.MergedSHA, nil
}

// --- git plumbing (scrubbed env; git only) ---

// nowFunc is the clock, overridable in tests for token-expiry cases.
var nowFunc = time.Now

// git runs a git subcommand with a minimal environment. Any leading config args
// (e.g. the token's http.extraHeader) are already included in args by the caller.
// A scrubbed env is the C13/C15 posture: the trusted process carries no inherited
// secrets.
func git(ctx context.Context, dir string, args ...string) (string, error) {
	cmd := exec.CommandContext(ctx, "git", args...)
	if dir != "" {
		cmd.Dir = dir
	}
	cmd.Env = scrubbedEnv()
	var out, errb strings.Builder
	cmd.Stdout = &out
	cmd.Stderr = &errb
	if err := cmd.Run(); err != nil {
		detail := strings.TrimSpace(errb.String())
		if detail == "" {
			detail = err.Error()
		}
		return errb.String(), fmt.Errorf("git %s: %s", redactArgs(args), detail)
	}
	return out.String(), nil
}

func revParse(ctx context.Context, dir, ref string) (string, error) {
	out, err := git(ctx, dir, "rev-parse", ref)
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(out), nil
}

// scrubbedEnv is the minimal environment git needs: PATH and HOME (for git to
// resolve its config and helpers), plus a non-interactive terminal posture so a
// credential prompt can never block. No API keys, no Artifacts token in the env.
func scrubbedEnv() []string {
	env := []string{
		"PATH=" + os.Getenv("PATH"),
		"HOME=" + os.Getenv("HOME"),
		"GIT_TERMINAL_PROMPT=0",
		"GIT_CONFIG_NOSYSTEM=1",
	}
	return env
}

// isLeaseFailure recognizes a `--force-with-lease` rejection (stale info / remote
// ref changed) as distinct from other push errors.
func isLeaseFailure(out string, err error) bool {
	s := out + " " + errString(err)
	return strings.Contains(s, "stale info") ||
		strings.Contains(s, "[rejected]") ||
		strings.Contains(s, "failed to push")
}

func errString(err error) string {
	if err == nil {
		return ""
	}
	return err.Error()
}

// redactArgs joins args for an error message, replacing any http.extraHeader
// value so the token is never printed.
func redactArgs(args []string) string {
	cp := make([]string, len(args))
	copy(cp, args)
	for i, a := range cp {
		if strings.HasPrefix(a, "http.extraHeader=") {
			cp[i] = "http.extraHeader=<redacted>"
		}
	}
	return strings.Join(cp, " ")
}

func short(sha string) string {
	if len(sha) > 10 {
		return sha[:10]
	}
	return sha
}

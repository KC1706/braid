package gitio

import (
	"context"
	"fmt"
	"sort"
	"strings"
)

// AgentRef is one agent fork to snapshot: its id, the remote it lives on, the
// branch the agent pushed, and the scoped token that reads it. Each fork has its
// own token, so each fetch authenticates with exactly the credential for that
// repo — the trusted zone holds them all, the compute zone receives none.
type AgentRef struct {
	ID     string
	Remote string
	Branch string
	Token  Token
}

// Snapshot is the result of assembling a prepared repo for the compute zone: a
// single self-contained bundle whose heads are `baseline` plus one `agent/<id>`
// per fork, and the SHAs those heads resolve to. The bundle is the only thing
// that crosses into the compute zone — no remotes, no tokens.
type Snapshot struct {
	BaselineSHA string
	// Branches maps an agent id to the SHA its `agent/<id>` head points at.
	Branches map[string]string
}

// SnapshotForks is the trusted zone's read half of a run. It clones the base
// branch into workdir (as the local head `baseline`), then fetches each agent's
// branch into the local head `agent/<id>` using that agent's own scoped token,
// and writes a multi-ref bundle to bundlePath. The compute zone opens that bundle
// with OpenPrepared and needs no network. workdir must be empty.
//
// Only git runs here, with a scrubbed env; a token is passed per-invocation via
// configArgs and never enters the environment or a persisted remote URL.
func SnapshotForks(ctx context.Context, baseRemote string, baseToken Token, baseBranch, workdir, bundlePath string, agents []AgentRef) (Snapshot, error) {
	if baseToken.Expired(nowFunc()) {
		return Snapshot{}, fmt.Errorf("gitio: base token expired (%s)", baseToken)
	}
	// Clone the base branch into a local head named `baseline` (not origin/*), so
	// the compute repo resolves `baseline` directly.
	cloneArgs := append(baseToken.configArgs(), "clone", "--branch", baseBranch, "--single-branch", baseRemote, workdir)
	if _, err := git(ctx, "", cloneArgs...); err != nil {
		return Snapshot{}, fmt.Errorf("gitio: clone base: %w", err)
	}
	if _, err := git(ctx, workdir, "branch", "-f", "baseline", "HEAD"); err != nil {
		return Snapshot{}, fmt.Errorf("gitio: name baseline: %w", err)
	}
	baselineSHA, err := revParse(ctx, workdir, "baseline")
	if err != nil {
		return Snapshot{}, err
	}

	snap := Snapshot{BaselineSHA: baselineSHA, Branches: map[string]string{}}
	heads := []string{"baseline"}
	for _, a := range agents {
		if a.ID == "" || a.Remote == "" || a.Branch == "" {
			return Snapshot{}, fmt.Errorf("gitio: agent ref needs id, remote and branch (got %+v)", redactRef(a))
		}
		if a.Token.Expired(nowFunc()) {
			return Snapshot{}, fmt.Errorf("gitio: token for agent %q expired (%s)", a.ID, a.Token)
		}
		local := "agent/" + a.ID
		refspec := a.Branch + ":" + local
		fetchArgs := append(a.Token.configArgs(), "fetch", a.Remote, refspec)
		if _, err := git(ctx, workdir, fetchArgs...); err != nil {
			return Snapshot{}, fmt.Errorf("gitio: fetch agent %q: %w", a.ID, err)
		}
		sha, err := revParse(ctx, workdir, local)
		if err != nil {
			return Snapshot{}, err
		}
		snap.Branches[a.ID] = sha
		heads = append(heads, local)
	}

	// A deterministic head order makes the bundle content-addressable across runs.
	sort.Strings(heads)
	bundleArgs := append([]string{"bundle", "create", bundlePath}, heads...)
	if _, err := git(ctx, workdir, bundleArgs...); err != nil {
		return Snapshot{}, fmt.Errorf("gitio: write prepared bundle: %w", err)
	}
	return snap, nil
}

// OpenPrepared initializes a fresh repo in workdir (which must be empty) from a
// prepared bundle written by SnapshotForks, importing every head (`baseline`,
// `agent/<id>`...) as a local branch of the same name. This is the compute zone's
// only git step: no network, no token, no remote. The returned repo is ready for
// run.Service with Baseline="baseline" and each agent Branch="agent/<id>".
func OpenPrepared(ctx context.Context, bundlePath, workdir string) error {
	// Bootstrap on a throwaway branch so no fetched head (baseline, agent/*) lands
	// on the checked-out branch, which git refuses to fetch into.
	if _, err := git(ctx, "", "init", "-q", "-b", "__braid_bootstrap__", workdir); err != nil {
		return fmt.Errorf("gitio: init prepared: %w", err)
	}
	// Fetch all heads out of the bundle into local branches of the same name.
	if _, err := git(ctx, workdir, "fetch", "-q", bundlePath, "refs/heads/*:refs/heads/*"); err != nil {
		return fmt.Errorf("gitio: open prepared bundle: %w", err)
	}
	// Leave the working tree on baseline so merge operations start from a clean base.
	if _, err := git(ctx, workdir, "checkout", "-q", "baseline"); err != nil {
		return fmt.Errorf("gitio: checkout baseline: %w", err)
	}
	return nil
}

// redactRef renders an AgentRef for an error without its token.
func redactRef(a AgentRef) string {
	return fmt.Sprintf("{ID:%q Remote:%q Branch:%q Token:%s}", a.ID, redactRemote(a.Remote), a.Branch, a.Token)
}

// redactRemote strips any in-URL userinfo from a remote before logging it.
func redactRemote(remote string) string {
	if i := strings.Index(remote, "@"); i >= 0 {
		if j := strings.Index(remote, "://"); j >= 0 && j < i {
			return remote[:j+3] + "<redacted>@" + remote[i+1:]
		}
	}
	return remote
}

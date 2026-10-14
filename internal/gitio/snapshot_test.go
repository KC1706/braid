package gitio

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// seedFork makes a bare "fork" remote off the base: clones it, commits a change
// on branch, pushes that branch, and returns (bareDir, branch, sha).
func seedFork(t *testing.T, base, branch, file, content string) (string, string) {
	t.Helper()
	root := t.TempDir()
	bare := filepath.Join(root, "fork.git")
	run(t, "", "clone", "--bare", base, bare)

	work := filepath.Join(root, "work")
	run(t, "", "clone", bare, work)
	run(t, work, "config", "user.email", "braid@example.com")
	run(t, work, "config", "user.name", "Braid")
	run(t, work, "checkout", "-q", "-B", branch)
	if err := os.WriteFile(filepath.Join(work, file), []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	run(t, work, "add", "-A")
	run(t, work, "commit", "-q", "-m", "fork "+branch)
	run(t, work, "push", "-q", "origin", branch)
	return bare, run(t, work, "rev-parse", "HEAD")
}

func TestSnapshotForksAndOpenPreparedRoundTrip(t *testing.T) {
	ctx := context.Background()
	base, baseSHA := seedRemote(t)

	fork1, sha1 := seedFork(t, base, "feature-a", "a.go", "package app\n\nvar A = 1\n")
	fork2, sha2 := seedFork(t, base, "feature-b", "b.go", "package app\n\nvar B = 2\n")

	work := filepath.Join(t.TempDir(), "io")
	bundlePath := filepath.Join(t.TempDir(), "prepared.bundle")
	snap, err := SnapshotForks(ctx, base, Token{}, "main", work, bundlePath, []AgentRef{
		{ID: "agent-1", Remote: fork1, Branch: "feature-a"},
		{ID: "agent-2", Remote: fork2, Branch: "feature-b"},
	})
	if err != nil {
		t.Fatalf("SnapshotForks: %v", err)
	}
	if snap.BaselineSHA != baseSHA {
		t.Fatalf("baseline sha = %s, want %s", snap.BaselineSHA, baseSHA)
	}
	if snap.Branches["agent-1"] != sha1 || snap.Branches["agent-2"] != sha2 {
		t.Fatalf("branch shas = %+v, want agent-1=%s agent-2=%s", snap.Branches, sha1, sha2)
	}

	// Compute zone opens the bundle with no network and gets local branches.
	repo := filepath.Join(t.TempDir(), "compute")
	if err := OpenPrepared(ctx, bundlePath, repo); err != nil {
		t.Fatalf("OpenPrepared: %v", err)
	}
	for ref, want := range map[string]string{"baseline": baseSHA, "agent/agent-1": sha1, "agent/agent-2": sha2} {
		got, err := revParse(ctx, repo, ref)
		if err != nil {
			t.Fatalf("rev-parse %s: %v", ref, err)
		}
		if got != want {
			t.Fatalf("opened %s = %s, want %s", ref, got, want)
		}
	}
	// The prepared repo must have NO remotes — the compute zone is offline.
	if out, _ := git(ctx, repo, "remote"); out != "" {
		t.Fatalf("prepared repo has remotes %q, want none", out)
	}
}

func TestSnapshotForksRejectsExpiredToken(t *testing.T) {
	ctx := context.Background()
	base, _ := seedRemote(t)
	// A token expired an hour ago (non-zero ExpiresAt in the past).
	expired := Token{Plaintext: "art_v2_x_dead", ExpiresAt: nowFunc().Add(-time.Hour)}
	_, err := SnapshotForks(ctx, base, expired, "main", filepath.Join(t.TempDir(), "io"), filepath.Join(t.TempDir(), "b.bundle"), nil)
	if err == nil {
		t.Fatal("want error for expired base token, got nil")
	}
}

package gitio

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// run executes git in dir and fails the test on error.
func run(t *testing.T, dir string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", args...)
	if dir != "" {
		cmd.Dir = dir
	}
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %s: %v\n%s", strings.Join(args, " "), err, out)
	}
	return strings.TrimSpace(string(out))
}

// seedRemote builds a bare "remote" repo with a baseline commit on branch main,
// via an intermediate working clone, and returns (bareDir, baseSHA).
func seedRemote(t *testing.T) (string, string) {
	t.Helper()
	root := t.TempDir()
	bare := filepath.Join(root, "remote.git")
	run(t, "", "init", "--bare", "-b", "main", bare)

	seed := filepath.Join(root, "seed")
	run(t, "", "clone", bare, seed)
	run(t, seed, "config", "user.email", "braid@example.com")
	run(t, seed, "config", "user.name", "Braid")
	if err := os.WriteFile(filepath.Join(seed, "app.go"), []byte("package app\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	run(t, seed, "add", "-A")
	run(t, seed, "commit", "-q", "-m", "baseline")
	run(t, seed, "push", "-q", "origin", "main")
	baseSHA := run(t, seed, "rev-parse", "HEAD")
	return bare, baseSHA
}

// computeMerge clones the remote, makes a "merged" commit on an integration
// branch, and returns (computeRepo, mergedSHA).
func computeMerge(t *testing.T, bare string) (string, string) {
	t.Helper()
	repo := filepath.Join(t.TempDir(), "compute")
	run(t, "", "clone", bare, repo)
	run(t, repo, "config", "user.email", "braid@example.com")
	run(t, repo, "config", "user.name", "Braid")
	run(t, repo, "checkout", "-q", "-B", "braid/integration")
	if err := os.WriteFile(filepath.Join(repo, "merged.go"), []byte("package app\n\nvar Merged = true\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	run(t, repo, "add", "-A")
	run(t, repo, "commit", "-q", "-m", "merged")
	return repo, run(t, repo, "rev-parse", "HEAD")
}

func TestWriteBundleAndPushPublishesMerge(t *testing.T) {
	ctx := context.Background()
	bare, baseSHA := seedRemote(t)
	compute, mergedSHA := computeMerge(t, bare)

	// Compute zone: export the content-addressed bundle.
	bundlePath := filepath.Join(t.TempDir(), "merge.bundle")
	b, err := WriteBundle(ctx, compute, "origin/main", "braid/integration", bundlePath)
	if err != nil {
		t.Fatal(err)
	}
	if b.BaseSHA != baseSHA || b.MergedSHA != mergedSHA {
		t.Fatalf("bundle shas: base=%s merged=%s (want %s / %s)", b.BaseSHA, b.MergedSHA, baseSHA, mergedSHA)
	}

	// I/O zone: clone base, push the bundle.
	zone := Zone{Remote: bare} // local path remote, zero token
	work := filepath.Join(t.TempDir(), "work")
	gotBase, err := zone.CloneBase(ctx, work, "main")
	if err != nil {
		t.Fatal(err)
	}
	if gotBase != baseSHA {
		t.Fatalf("CloneBase base = %s, want %s", gotBase, baseSHA)
	}
	pushed, err := zone.PushBundle(ctx, work, "main", b)
	if err != nil {
		t.Fatal(err)
	}
	if pushed != mergedSHA {
		t.Fatalf("pushed = %s, want %s", pushed, mergedSHA)
	}
	// The remote's main now points at the merged commit.
	if tip := run(t, "", "--git-dir", bare, "rev-parse", "main"); tip != mergedSHA {
		t.Fatalf("remote main = %s, want %s", tip, mergedSHA)
	}
}

func TestPushBundleCASRejectsMovedBaseBeforeClone(t *testing.T) {
	ctx := context.Background()
	bare, _ := seedRemote(t)
	compute, _ := computeMerge(t, bare)
	bundlePath := filepath.Join(t.TempDir(), "merge.bundle")
	b, err := WriteBundle(ctx, compute, "origin/main", "braid/integration", bundlePath)
	if err != nil {
		t.Fatal(err)
	}

	// A concurrent run advances the remote base BEFORE we clone.
	advanceRemote(t, bare)

	zone := Zone{Remote: bare}
	work := filepath.Join(t.TempDir(), "work")
	if _, err := zone.CloneBase(ctx, work, "main"); err != nil {
		t.Fatal(err)
	}
	// The freshly cloned base no longer matches the bundle's recorded base.
	_, err = zone.PushBundle(ctx, work, "main", b)
	if !errors.Is(err, ErrBaseMoved) {
		t.Fatalf("want ErrBaseMoved, got %v", err)
	}
}

func TestPushBundleCASRejectsMovedBaseAfterClone(t *testing.T) {
	ctx := context.Background()
	bare, _ := seedRemote(t)
	compute, _ := computeMerge(t, bare)
	bundlePath := filepath.Join(t.TempDir(), "merge.bundle")
	b, err := WriteBundle(ctx, compute, "origin/main", "braid/integration", bundlePath)
	if err != nil {
		t.Fatal(err)
	}

	zone := Zone{Remote: bare}
	work := filepath.Join(t.TempDir(), "work")
	if _, err := zone.CloneBase(ctx, work, "main"); err != nil {
		t.Fatal(err)
	}
	// A concurrent run advances the remote AFTER we cloned: local precheck passes,
	// but the lease push must be rejected atomically by the remote.
	advanceRemote(t, bare)

	_, err = zone.PushBundle(ctx, work, "main", b)
	if !errors.Is(err, ErrBaseMoved) {
		t.Fatalf("want ErrBaseMoved from lease rejection, got %v", err)
	}
}

func TestPushBundleRejectsNonDescendant(t *testing.T) {
	ctx := context.Background()
	bare, baseSHA := seedRemote(t)

	// Build an ORPHAN branch (unrelated history) in a compute clone.
	repo := filepath.Join(t.TempDir(), "compute")
	run(t, "", "clone", bare, repo)
	run(t, repo, "config", "user.email", "braid@example.com")
	run(t, repo, "config", "user.name", "Braid")
	run(t, repo, "checkout", "-q", "--orphan", "rogue")
	run(t, repo, "rm", "-rf", "--cached", ".")
	if err := os.WriteFile(filepath.Join(repo, "rogue.go"), []byte("package app\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	run(t, repo, "add", "-A")
	run(t, repo, "commit", "-q", "-m", "rogue root")
	bundlePath := filepath.Join(t.TempDir(), "rogue.bundle")
	b, err := WriteBundle(ctx, repo, "origin/main", "rogue", bundlePath)
	if err != nil {
		t.Fatal(err)
	}
	if b.BaseSHA != baseSHA {
		t.Fatalf("bundle base = %s, want %s", b.BaseSHA, baseSHA)
	}

	zone := Zone{Remote: bare}
	work := filepath.Join(t.TempDir(), "work")
	if _, err := zone.CloneBase(ctx, work, "main"); err != nil {
		t.Fatal(err)
	}
	// Local base matches (precheck passes) but the merged commit does not descend
	// from it, so the ancestry guard must reject it (never graft unrelated work).
	_, err = zone.PushBundle(ctx, work, "main", b)
	if err == nil || !strings.Contains(err.Error(), "does not descend") {
		t.Fatalf("want descent rejection, got %v", err)
	}
	// The remote is untouched.
	if tip := run(t, "", "--git-dir", bare, "rev-parse", "main"); tip != baseSHA {
		t.Fatalf("remote main should be unchanged, got %s", tip)
	}
}

// advanceRemote pushes an unrelated commit to the remote's main, simulating a
// concurrent run that advanced the base.
func advanceRemote(t *testing.T, bare string) {
	t.Helper()
	other := filepath.Join(t.TempDir(), "other")
	run(t, "", "clone", bare, other)
	run(t, other, "config", "user.email", "other@example.com")
	run(t, other, "config", "user.name", "Other")
	if err := os.WriteFile(filepath.Join(other, "other.go"), []byte("package app\n\nvar Other = 1\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	run(t, other, "add", "-A")
	run(t, other, "commit", "-q", "-m", "concurrent advance")
	run(t, other, "push", "-q", "origin", "main")
}

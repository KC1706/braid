package merge

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/KC1706/braid/internal/ladder"
	"github.com/KC1706/braid/pkg/gitx"
	"github.com/KC1706/braid/pkg/models"
)

// newLadder builds the Go verification ladder used by the integration tests.
func newLadder() *ladder.Ladder {
	return &ladder.Ladder{Build: ladder.GoBuildVerifier{}, Test: ladder.GoTestVerifier{}}
}

// initRepo creates an empty git repo configured for Braid in a temp dir.
func initRepo(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	mustGit(t, dir, "init", "-q")
	mustGit(t, dir, "config", "user.email", "braid@example.com")
	mustGit(t, dir, "config", "user.name", "Braid")
	mustGit(t, dir, "config", "merge.conflictStyle", "diff3")
	return dir
}

// commitBaseline writes the baseline files, commits them, and creates the
// `baseline` branch.
func commitBaseline(t *testing.T, dir string, files map[string]string) {
	t.Helper()
	writeFiles(t, dir, files)
	mustGit(t, dir, "add", "-A")
	mustGit(t, dir, "commit", "-q", "-m", "baseline")
	mustGit(t, dir, "branch", "baseline")
}

// makeBranch creates branch from baseline, applies the given file writes, and
// commits. It leaves HEAD on that branch.
func makeBranch(t *testing.T, dir, branch string, files map[string]string) {
	t.Helper()
	mustGit(t, dir, "checkout", "-q", "-B", branch, "baseline")
	writeFiles(t, dir, files)
	mustGit(t, dir, "add", "-A")
	mustGit(t, dir, "commit", "-q", "-m", branch)
}

func writeFiles(t *testing.T, dir string, files map[string]string) {
	t.Helper()
	for rel, content := range files {
		p := filepath.Join(dir, rel)
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
}

func mustGit(t *testing.T, dir string, args ...string) {
	t.Helper()
	if _, err := gitx.Ok(dir, args...); err != nil {
		t.Fatalf("git %s: %v", strings.Join(args, " "), err)
	}
}

// readFileRel reads a repo-relative file from the working tree.
func readFileRel(t *testing.T, dir, rel string) string {
	t.Helper()
	b, err := os.ReadFile(filepath.Join(dir, rel))
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

// readTrail loads the written .braid/trail.json from the working tree.
func readTrail(t *testing.T, dir string) models.Trail {
	t.Helper()
	var tr models.Trail
	b, err := os.ReadFile(filepath.Join(dir, ".braid", "trail.json"))
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(b, &tr); err != nil {
		t.Fatalf("trail.json parse: %v", err)
	}
	return tr
}

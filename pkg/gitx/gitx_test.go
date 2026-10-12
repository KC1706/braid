package gitx

import (
	"os"
	"path/filepath"
	"testing"
)

// initRepo makes a throwaway git repo with one commit on branch "baseline".
func initRepo(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	for _, args := range [][]string{
		{"init", "-q"},
		{"config", "user.email", "braid@example.com"},
		{"config", "user.name", "Braid"},
		{"checkout", "-b", "baseline"},
	} {
		if _, err := Ok(dir, args...); err != nil {
			t.Fatalf("setup %v: %v", args, err)
		}
	}
	if err := os.WriteFile(filepath.Join(dir, "a.txt"), []byte("hello\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := Ok(dir, "add", "-A"); err != nil {
		t.Fatal(err)
	}
	if _, err := Ok(dir, "commit", "-q", "-m", "init"); err != nil {
		t.Fatal(err)
	}
	return dir
}

func TestRunNonZeroIsNotAnError(t *testing.T) {
	dir := initRepo(t)
	// A command that legitimately exits non-zero must report a Code, not blow up.
	r := Run(dir, "rev-parse", "does-not-exist")
	if r.Code == 0 {
		t.Fatalf("expected non-zero code, got %d", r.Code)
	}
}

func TestCurrentSHAAndLsFiles(t *testing.T) {
	dir := initRepo(t)
	sha, err := CurrentSHA(dir)
	if err != nil || len(sha) < 7 {
		t.Fatalf("CurrentSHA: %q err=%v", sha, err)
	}
	files, err := LsFiles(dir, "baseline")
	if err != nil {
		t.Fatal(err)
	}
	if len(files) != 1 || files[0] != "a.txt" {
		t.Fatalf("LsFiles = %v", files)
	}
}

func TestShowFileExistsAndMissing(t *testing.T) {
	dir := initRepo(t)
	content, exists, err := ShowFile(dir, "baseline", "a.txt")
	if err != nil || !exists || content != "hello\n" {
		t.Fatalf("ShowFile existing: content=%q exists=%v err=%v", content, exists, err)
	}
	_, exists, err = ShowFile(dir, "baseline", "nope.txt")
	if err != nil || exists {
		t.Fatalf("ShowFile missing: exists=%v err=%v (want exists=false, err=nil)", exists, err)
	}
}

func TestOkReturnsErrorWithContext(t *testing.T) {
	dir := initRepo(t)
	_, err := Ok(dir, "checkout", "no-such-branch")
	if err == nil {
		t.Fatal("expected error from Ok on a failing command")
	}
}

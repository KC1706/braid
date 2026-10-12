package ladder

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/KC1706/braid/pkg/gitx"
	"github.com/KC1706/braid/pkg/models"
)

func TestCountTestFuncs(t *testing.T) {
	cases := []struct {
		name string
		src  string
		want int
	}{
		{"test and benchmark and fuzz", "func TestA(t *testing.T){}\nfunc BenchmarkB(b *testing.B){}\nfunc FuzzC(f *testing.F){}", 3},
		{"subtests count", "func TestA(t *testing.T){\n t.Run(\"x\", func(t *testing.T){})\n t.Run(\"y\", func(t *testing.T){})\n}", 3},
		{"commented-out tests ignored", "// func TestA(t *testing.T){}\n/* func TestB(t *testing.T){} */\nfunc TestC(t *testing.T){}", 1},
		{"not a test func", "func testHelper(){}\nfunc Testish(){}", 1}, // Testish matches Test\w* — intentional (over-count is safe for a guard)
		{"none", "package p\nfunc Add(a,b int) int { return a+b }", 0},
	}
	for _, c := range cases {
		if got := CountTestFuncs(c.src); got != c.want {
			t.Errorf("%s: CountTestFuncs = %d, want %d", c.name, got, c.want)
		}
	}
}

func TestIsTestFile(t *testing.T) {
	if !IsTestFile("pkg/foo/foo_test.go") {
		t.Error("foo_test.go should be a test file")
	}
	if IsTestFile("pkg/foo/foo.go") {
		t.Error("foo.go is not a test file")
	}
}

// newFixtureRepo builds a git repo with a nested Go module and a `baseline`
// branch holding one source file (Add) and one test file (TestAdd). The working
// tree is checked out at baseline; callers mutate it on disk, then run the rung.
func newFixtureRepo(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	write(t, dir, "go.mod", "module example.com/fixture\n\ngo 1.26\n")
	write(t, dir, "add.go", "package fixture\n\nfunc Add(a, b int) int { return a + b }\n")
	write(t, dir, "add_test.go", "package fixture\n\nimport \"testing\"\n\nfunc TestAdd(t *testing.T) {\n\tif Add(1, 2) != 3 {\n\t\tt.Fatal(\"bad\")\n\t}\n}\n")

	mustGit(t, dir, "init", "-q")
	mustGit(t, dir, "config", "user.email", "braid@example.com")
	mustGit(t, dir, "config", "user.name", "braid")
	mustGit(t, dir, "config", "merge.conflictStyle", "diff3")
	mustGit(t, dir, "add", ".")
	mustGit(t, dir, "commit", "-q", "-m", "baseline")
	mustGit(t, dir, "branch", "baseline")
	return dir
}

func TestStructuralGuardsPassesUntouched(t *testing.T) {
	dir := newFixtureRepo(t)
	r, err := StructuralGuards(dir, "baseline")
	if err != nil {
		t.Fatal(err)
	}
	if !r.Passed {
		t.Fatalf("untouched tree should pass: %s\n%s", r.Summary, r.Detail)
	}
}

func TestStructuralGuardsRejectsDeletedTest(t *testing.T) {
	dir := newFixtureRepo(t)
	if err := os.Remove(filepath.Join(dir, "add_test.go")); err != nil {
		t.Fatal(err)
	}
	r, _ := StructuralGuards(dir, "baseline")
	if r.Passed {
		t.Fatal("deleting a test file must be rejected (C2)")
	}
	if !strings.Contains(r.Detail, "test file deleted") {
		t.Errorf("detail = %q", r.Detail)
	}
}

func TestStructuralGuardsRejectsWeakenedTest(t *testing.T) {
	dir := newFixtureRepo(t)
	// Rewrite the test file down to zero executable test cases.
	write(t, dir, "add_test.go", "package fixture\n// all tests removed\n")
	r, _ := StructuralGuards(dir, "baseline")
	if r.Passed {
		t.Fatal("weakening the test suite must be rejected (C2)")
	}
	if !strings.Contains(r.Detail, "coverage dropped") && !strings.Contains(r.Detail, "test cases dropped") {
		t.Errorf("detail = %q", r.Detail)
	}
}

func TestStructuralGuardsAllowsAddingTests(t *testing.T) {
	dir := newFixtureRepo(t)
	write(t, dir, "extra_test.go", "package fixture\n\nimport \"testing\"\n\nfunc TestExtra(t *testing.T) {}\n")
	r, _ := StructuralGuards(dir, "baseline")
	if !r.Passed {
		t.Fatalf("adding tests should be allowed: %s", r.Detail)
	}
}

func TestGoBuildAndTestVerifiersGreen(t *testing.T) {
	dir := newFixtureRepo(t)
	ctx := context.Background()
	b, err := GoBuildVerifier{VetToo: true}.Verify(ctx, dir)
	if err != nil {
		t.Fatal(err)
	}
	if !b.Passed {
		t.Fatalf("build should pass: %s\n%s", b.Summary, b.Detail)
	}
	tr, err := GoTestVerifier{}.Verify(ctx, dir)
	if err != nil {
		t.Fatal(err)
	}
	if !tr.Passed {
		t.Fatalf("tests should pass: %s\n%s", tr.Summary, tr.Detail)
	}
	if !strings.Contains(tr.Summary, "1 passed") {
		t.Errorf("summary = %q", tr.Summary)
	}
}

func TestGoBuildVerifierCatchesBrokenReference(t *testing.T) {
	dir := newFixtureRepo(t)
	// A clean-but-broken merge: reference a symbol that no longer exists.
	write(t, dir, "caller.go", "package fixture\n\nfunc UseAdd() int { return Addd(1, 2) }\n")
	b, _ := GoBuildVerifier{}.Verify(context.Background(), dir)
	if b.Passed {
		t.Fatal("build must fail on an undefined reference (C1)")
	}
	if !strings.Contains(b.Detail, "Addd") && !strings.Contains(b.Detail, "undefined") {
		t.Errorf("detail should name the break: %q", b.Detail)
	}
}

func TestLadderShortCircuitsAtBuildBeforeTests(t *testing.T) {
	dir := newFixtureRepo(t)
	write(t, dir, "caller.go", "package fixture\n\nfunc UseAdd() int { return Addd(1, 2) }\n")
	l := &Ladder{Build: GoBuildVerifier{}, Test: GoTestVerifier{}}
	res, err := l.Run(context.Background(), dir, "baseline")
	if err != nil {
		t.Fatal(err)
	}
	if res.Verdict != "red" {
		t.Fatalf("verdict = %q, want red", res.Verdict)
	}
	// The deciding rung is build (Rung 2); test execution never ran.
	if names(res.Rungs)["build"] == false {
		t.Error("build rung should be present")
	}
	if names(res.Rungs)["test-execution"] {
		t.Error("test execution must be skipped when build fails")
	}
}

func TestLadderGreenEndToEnd(t *testing.T) {
	dir := newFixtureRepo(t)
	l := &Ladder{Build: GoBuildVerifier{}, Test: GoTestVerifier{}}
	res, err := l.Run(context.Background(), dir, "baseline")
	if err != nil {
		t.Fatal(err)
	}
	if res.Verdict != "green" || !res.Passed {
		t.Fatalf("verdict = %q passed=%v\n%s", res.Verdict, res.Passed, res.Output)
	}
	if len(res.Rungs) != 3 {
		t.Errorf("want guard+build+test = 3 rungs, got %d", len(res.Rungs))
	}
}

// helpers

func names(rungs []models.RungResult) map[string]bool {
	m := map[string]bool{}
	for _, r := range rungs {
		m[r.Name] = true
	}
	return m
}

func write(t *testing.T, dir, rel, content string) {
	t.Helper()
	p := filepath.Join(dir, rel)
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

func mustGit(t *testing.T, dir string, args ...string) {
	t.Helper()
	if _, err := gitx.Ok(dir, args...); err != nil {
		t.Fatalf("git %s: %v", strings.Join(args, " "), err)
	}
}

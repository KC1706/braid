package ladder

import (
	"strings"
	"testing"

	"github.com/KC1706/braid/pkg/models"
)

// conflicted builds a diff3-style conflicted file: prefix, one conflict region,
// suffix. Used to exercise the confinement guard.
func conflicted(prefix, ours, theirs, suffix string) string {
	var b strings.Builder
	if prefix != "" {
		b.WriteString(prefix + "\n")
	}
	b.WriteString("<<<<<<< ours\n")
	b.WriteString(ours + "\n")
	b.WriteString("=======\n")
	b.WriteString(theirs + "\n")
	b.WriteString(">>>>>>> theirs\n")
	b.WriteString(suffix)
	return b.String()
}

func TestConfineToConflictRegions(t *testing.T) {
	original := conflicted("package p", "x := 1", "x := 2", "const k = 3")

	t.Run("in-region replacement accepted", func(t *testing.T) {
		resolved := "package p\nx := 1\nx := 2\nconst k = 3"
		if err := ConfineToConflictRegions(original, resolved); err != nil {
			t.Fatalf("legitimate in-region resolution rejected: %v", err)
		}
	})

	t.Run("out-of-hunk edit before region rejected", func(t *testing.T) {
		resolved := "package EVIL\nx := 1\nx := 2\nconst k = 3"
		if err := ConfineToConflictRegions(original, resolved); err == nil {
			t.Fatal("edit to the prefix line was not rejected")
		}
	})

	t.Run("out-of-hunk edit after region rejected", func(t *testing.T) {
		resolved := "package p\nx := 1\nx := 2\nconst k = 999"
		if err := ConfineToConflictRegions(original, resolved); err == nil {
			t.Fatal("edit to the suffix line was not rejected")
		}
	})

	t.Run("deleting a surrounding line rejected", func(t *testing.T) {
		resolved := "package p\nx := 1" // suffix line gone entirely
		if err := ConfineToConflictRegions(original, resolved); err == nil {
			t.Fatal("deletion of the suffix line was not rejected")
		}
	})

	t.Run("no-conflict file must be identical", func(t *testing.T) {
		plain := "package p\nfunc A() {}\n"
		if err := ConfineToConflictRegions(plain, plain); err != nil {
			t.Fatalf("identical no-conflict file rejected: %v", err)
		}
		if err := ConfineToConflictRegions(plain, plain+"// sneaky\n"); err == nil {
			t.Fatal("edit to a no-conflict file was not rejected")
		}
	})
}

func TestConfineMultipleRegions(t *testing.T) {
	original := "a\n<<<<<<< ours\no1\n=======\nt1\n>>>>>>> theirs\nb\n<<<<<<< ours\no2\n=======\nt2\n>>>>>>> theirs\nc"
	ok := "a\no1\nt1\nb\no2\nt2\nc"
	if err := ConfineToConflictRegions(original, ok); err != nil {
		t.Fatalf("valid two-region resolution rejected: %v", err)
	}
	// Edit the middle spine line "b" → must be rejected (it is outside both hunks).
	bad := "a\no1\nt1\nBBB\no2\nt2\nc"
	if err := ConfineToConflictRegions(original, bad); err == nil {
		t.Fatal("edit to the between-regions spine line was not rejected")
	}
}

func TestCheckResolutionBounds(t *testing.T) {
	hunk := models.ConflictHunk{
		Ours:   []string{"x := 1"},
		Base:   []string{"x := 0"},
		Theirs: []string{"x := 2"},
	}

	t.Run("union-sized resolution accepted", func(t *testing.T) {
		if err := CheckResolutionBounds(hunk, "x := 1\nx := 2\nx := 0"); err != nil {
			t.Fatalf("in-bounds resolution rejected: %v", err)
		}
	})

	t.Run("smuggled conflict marker rejected", func(t *testing.T) {
		if err := CheckResolutionBounds(hunk, "x := 1\n>>>>>>> theirs"); err == nil {
			t.Fatal("resolution reintroducing a conflict marker was not rejected")
		}
	})

	t.Run("ballooned resolution rejected", func(t *testing.T) {
		huge := strings.Repeat("junk\n", 500)
		if err := CheckResolutionBounds(hunk, huge); err == nil {
			t.Fatal("out-of-bounds (ballooned) resolution was not rejected")
		}
	})
}

func TestParsesAsGo(t *testing.T) {
	good := "package p\n\nfunc A() int { return 1 }\n"
	if err := ParsesAsGo("users.go", good); err != nil {
		t.Fatalf("valid Go rejected: %v", err)
	}
	bad := "package p\n\nfunc A() int { return " // truncated garbage
	if err := ParsesAsGo("users.go", bad); err == nil {
		t.Fatal("non-parsing Go output was not rejected")
	}
	// Non-Go paths are not parse-checked.
	if err := ParsesAsGo("README.md", bad); err != nil {
		t.Fatalf("non-Go path should skip the parse check: %v", err)
	}
}

func TestGuardResolution(t *testing.T) {
	// A realistic .go file with one conflict region.
	original := "package p\n\nfunc getUser(id string) string {\n<<<<<<< ours\n\tvalidate(id)\n=======\n\trateLimit(id)\n>>>>>>> theirs\n\treturn id\n}\n"
	hunks := []models.ConflictHunk{{
		Ours:   []string{"\tvalidate(id)"},
		Theirs: []string{"\trateLimit(id)"},
	}}

	t.Run("clean union resolution passes", func(t *testing.T) {
		resolved := "package p\n\nfunc getUser(id string) string {\n\tvalidate(id)\n\trateLimit(id)\n\treturn id\n}\n"
		if err := GuardResolution("users.go", original, resolved, hunks, []string{"\tvalidate(id)\n\trateLimit(id)"}); err != nil {
			t.Fatalf("clean resolution rejected: %v", err)
		}
	})

	t.Run("out-of-region edit fails", func(t *testing.T) {
		resolved := "package EVIL\n\nfunc getUser(id string) string {\n\tvalidate(id)\n\trateLimit(id)\n\treturn id\n}\n"
		if err := GuardResolution("users.go", original, resolved, hunks, []string{"\tvalidate(id)\n\trateLimit(id)"}); err == nil {
			t.Fatal("out-of-region edit passed the combined guard")
		}
	})

	t.Run("non-parsing result fails", func(t *testing.T) {
		resolved := "package p\n\nfunc getUser(id string) string {\n\tvalidate(id\n\treturn id\n}\n"
		if err := GuardResolution("users.go", original, resolved, hunks, []string{"\tvalidate(id"}); err == nil {
			t.Fatal("non-parsing result passed the combined guard")
		}
	})
}

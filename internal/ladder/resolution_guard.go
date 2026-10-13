package ladder

// This file adds the Phase-2a resolution guards (GO_PRODUCTION_PLAN.md §3 Phase 2
// / §4, PRODUCTION_PLAN.md §5.1). They harden the rung against a resolver — the
// deterministic union, or an LLM — that returns more than it was asked for:
//
//	(i)  ConfineToConflictRegions — the applied file must edit NOTHING outside the
//	     conflict regions it was handed. A resolution that rewrites a surrounding
//	     line is rejected before the tests ever run.
//	(ii) ParsesAsGo + CheckResolutionBounds — AST bounds on resolver/LLM output
//	     (C14): a .go result must still parse with go/parser, each hunk's
//	     replacement must stay within a declared size budget, and it may not
//	     smuggle a fresh conflict marker.
//
// These run on whatever a resolver proposes, cheaper than build/test, so a
// malformed or out-of-scope resolution is caught and fed back (retry → escalate)
// rather than trusted. The structural-guards rung (test-file deletion /
// test-count regression) is unchanged and lives in ladder.go.

import (
	"fmt"
	"go/parser"
	"go/token"
	"regexp"
	"strings"

	"github.com/KC1706/braid/pkg/models"
)

// Conflict-marker matchers, duplicated here (not imported from internal/merge) so
// the ladder stays free of a merge→ladder→merge import cycle. They mirror the
// diff3 markers merge.ParseConflicts produces.
var (
	reConflictStart = regexp.MustCompile(`^<<<<<<< `)
	reConflictSep   = regexp.MustCompile(`^=======\s*$`)
	reConflictBase  = regexp.MustCompile(`^\|\|\|\|\|\|\| `)
	reConflictEnd   = regexp.MustCompile(`^>>>>>>> `)
)

// Resolution bound knobs (C14). A union keeps at most ours+base+theirs; the slack
// lets an LLM reflow/reword without ballooning into an injected payload.
const (
	resolutionGrowthFactor = 4
	resolutionLineBudget   = 50
)

// conflictSpine splits original into its non-conflict "spine": the runs of lines
// that lie OUTSIDE every conflict region, in order. With n conflict regions the
// spine has n+1 segments (any of which may be empty: a leading/trailing conflict,
// or two adjacent conflicts). These segments are exactly what a resolution must
// leave untouched.
func conflictSpine(original string) (spine [][]string, nConflicts int) {
	lines := strings.Split(original, "\n")
	var cur []string
	for i := 0; i < len(lines); {
		if reConflictStart.MatchString(lines[i]) {
			spine = append(spine, cur)
			cur = nil
			nConflicts++
			for i < len(lines) && !reConflictEnd.MatchString(lines[i]) {
				i++
			}
			if i < len(lines) { // consume the >>>>>>> line
				i++
			}
			continue
		}
		cur = append(cur, lines[i])
		i++
	}
	spine = append(spine, cur)
	return spine, nConflicts
}

func blockEqualAt(lines []string, start int, block []string) bool {
	if start < 0 || start+len(block) > len(lines) {
		return false
	}
	for j, b := range block {
		if lines[start+j] != b {
			return false
		}
	}
	return true
}

// findBlock returns the first index >= from where block matches exactly, or -1.
// An empty block matches at from.
func findBlock(lines []string, from int, block []string) int {
	if len(block) == 0 {
		return from
	}
	for i := from; i+len(block) <= len(lines); i++ {
		if blockEqualAt(lines, i, block) {
			return i
		}
	}
	return -1
}

// ConfineToConflictRegions verifies that resolved differs from original ONLY
// inside the conflict regions — every non-conflict (spine) line of original is
// preserved verbatim and in order, with the replacements occupying exactly the
// gaps the conflict regions left. A resolution that edits a surrounding line
// breaks an anchor and is rejected. This is the "confine resolver edits to the
// conflict region" guard (GO_PRODUCTION_PLAN.md §4 2a-i).
func ConfineToConflictRegions(original, resolved string) error {
	spine, n := conflictSpine(original)
	if n == 0 {
		if resolved != original {
			return fmt.Errorf("resolution modified a file that had no conflict region")
		}
		return nil
	}

	rlines := strings.Split(resolved, "\n")

	// The first spine segment must be an exact prefix of the resolved file.
	if !blockEqualAt(rlines, 0, spine[0]) {
		return fmt.Errorf("resolution edited content before the first conflict region")
	}
	pos := len(spine[0])

	for k := 1; k < len(spine); k++ {
		seg := spine[k]
		if k == len(spine)-1 {
			// The last spine segment must be the exact suffix; the lines between
			// pos and it are the final conflict region's replacement.
			start := len(rlines) - len(seg)
			if start < pos || !blockEqualAt(rlines, start, seg) {
				return fmt.Errorf("resolution edited content after the last conflict region")
			}
			return nil
		}
		// A middle spine segment must reappear in order after the preceding
		// replacement gap. Losing the anchor means a non-conflict line changed.
		idx := findBlock(rlines, pos, seg)
		if idx < 0 {
			return fmt.Errorf("resolution edited content outside conflict region %d (spine anchor lost)", k)
		}
		pos = idx + len(seg)
	}
	return nil
}

// CheckResolutionBounds enforces size bounds on one hunk's replacement and
// rejects a resolution that reintroduces a conflict marker (which would smuggle a
// fresh region past the splice). Part of the C14 AST/output-bounds defense.
func CheckResolutionBounds(hunk models.ConflictHunk, resolution string) error {
	for _, l := range strings.Split(resolution, "\n") {
		if reConflictStart.MatchString(l) || reConflictSep.MatchString(l) ||
			reConflictBase.MatchString(l) || reConflictEnd.MatchString(l) {
			return fmt.Errorf("resolution reintroduced a conflict marker")
		}
	}
	budget := resolutionGrowthFactor*(len(hunk.Ours)+len(hunk.Base)+len(hunk.Theirs)) + resolutionLineBudget
	if got := len(strings.Split(resolution, "\n")); got > budget {
		return fmt.Errorf("resolution out of bounds: %d line(s) exceeds budget %d (C14)", got, budget)
	}
	return nil
}

// ParsesAsGo rejects a resolved .go file that no longer parses (C14). Non-Go
// paths pass. The merged file is a complete source file, so a full parse is the
// right check; it is cheaper than the build rung and fails fast on LLM garbage.
func ParsesAsGo(path, src string) error {
	if !strings.HasSuffix(path, ".go") {
		return nil
	}
	fset := token.NewFileSet()
	if _, err := parser.ParseFile(fset, path, src, parser.SkipObjectResolution); err != nil {
		return fmt.Errorf("resolved %s does not parse as Go: %w", path, err)
	}
	return nil
}

// GuardResolution is the combined Phase-2a gate the merge engine calls on every
// proposed resolution before it is trusted: edits confined to the conflict
// regions, each replacement within bounds and marker-free, and (for Go) still
// parseable. original is the conflicted file as handed to the resolver, resolved
// is ApplyResolutions(original, resolutions), hunks are the parsed regions and
// resolutions[k] is the replacement for hunks[k]. A non-nil error means reject.
func GuardResolution(path, original, resolved string, hunks []models.ConflictHunk, resolutions []string) error {
	if err := ConfineToConflictRegions(original, resolved); err != nil {
		return err
	}
	for i, r := range resolutions {
		var h models.ConflictHunk
		if i < len(hunks) {
			h = hunks[i]
		}
		if err := CheckResolutionBounds(h, r); err != nil {
			return fmt.Errorf("hunk %d: %w", i, err)
		}
	}
	return ParsesAsGo(path, resolved)
}

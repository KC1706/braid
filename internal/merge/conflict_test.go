package merge

import (
	"reflect"
	"strings"
	"testing"
)

const sample = `export function getUser(id) {
<<<<<<< ours
  validateId(id);
  return db.find(id);
||||||| base
  return db.find(id);
=======
  rateLimit(id);
  return db.find(id);
>>>>>>> theirs
}`

func TestParseConflictsExtractsDiff3Regions(t *testing.T) {
	hs := ParseConflicts(sample)
	if len(hs) != 1 {
		t.Fatalf("want 1 hunk, got %d", len(hs))
	}
	h := hs[0]
	if !reflect.DeepEqual(h.Ours, []string{"  validateId(id);", "  return db.find(id);"}) {
		t.Errorf("ours = %#v", h.Ours)
	}
	if !reflect.DeepEqual(h.Base, []string{"  return db.find(id);"}) {
		t.Errorf("base = %#v", h.Base)
	}
	if !reflect.DeepEqual(h.Theirs, []string{"  rateLimit(id);", "  return db.find(id);"}) {
		t.Errorf("theirs = %#v", h.Theirs)
	}
	if !contains(h.ContextBefore, "export function getUser(id) {") {
		t.Errorf("contextBefore = %#v", h.ContextBefore)
	}
	if !contains(h.ContextAfter, "}") {
		t.Errorf("contextAfter = %#v", h.ContextAfter)
	}
	if h.TheirsLabel != "theirs" {
		t.Errorf("theirsLabel = %q", h.TheirsLabel)
	}
}

func TestApplyResolutionsSplicesBack(t *testing.T) {
	// A hand-written 3-way union (the real UnionResolver arrives in step 5).
	resolution := "  validateId(id);\n  rateLimit(id);\n  return db.find(id);"
	merged := ApplyResolutions(sample, []string{resolution})
	if strings.Contains(merged, "<<<<<<<") {
		t.Errorf("markers survived:\n%s", merged)
	}
	if !strings.Contains(merged, resolution) {
		t.Errorf("resolution not spliced in:\n%s", merged)
	}
	if !strings.HasPrefix(merged, "export function getUser(id) {") {
		t.Errorf("prefix lost:\n%s", merged)
	}
	if !strings.HasSuffix(strings.TrimRight(merged, "\n"), "}") {
		t.Errorf("suffix lost:\n%s", merged)
	}
}

func TestApplyResolutionsEmptyDropsRegion(t *testing.T) {
	merged := ApplyResolutions(sample, []string{""})
	if strings.Contains(merged, "<<<<<<<") || strings.Contains(merged, "validateId") {
		t.Errorf("empty resolution should drop the whole region:\n%s", merged)
	}
	if !strings.HasPrefix(merged, "export function getUser(id) {") {
		t.Errorf("surrounding text should remain:\n%s", merged)
	}
}

func TestHasConflictMarkers(t *testing.T) {
	if !HasConflictMarkers(sample) {
		t.Error("should detect markers in sample")
	}
	if HasConflictMarkers("no markers here\njust text") {
		t.Error("false positive on clean text")
	}
}

func contains(ss []string, want string) bool {
	for _, s := range ss {
		if s == want {
			return true
		}
	}
	return false
}

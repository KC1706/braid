// Package merge is Braid's 3-way merge driver and agent-integration loop (the
// Go port of the TS integrator's conflict.ts and integrate.ts). This file ports
// conflict.ts: the diff3 conflict-marker parser and the splice-back applier.
package merge

import (
	"regexp"
	"strings"

	"github.com/KC1706/braid/pkg/models"
)

// ConflictHunk is one diff3 conflict region. It is an alias for
// models.ConflictHunk, which lives in the shared-types package so the resolver
// can consume it without an import cycle (merge → resolver → merge). The base
// region (present only with merge.conflictStyle=diff3) is what lets Braid do a
// precise 3-way union instead of a blind pick.
//
//	<<<<<<< ours
//	...ours...
//	||||||| base
//	...base...
//	=======
//	...theirs...
//	>>>>>>> theirs
type ConflictHunk = models.ConflictHunk

var (
	reOurs   = regexp.MustCompile(`^<<<<<<< (.*)$`)
	reBase   = regexp.MustCompile(`^\|\|\|\|\|\|\| (.*)$`)
	reSep    = regexp.MustCompile(`^=======\s*$`)
	reTheirs = regexp.MustCompile(`^>>>>>>> (.*)$`)
)

const contextLines = 3

// HasConflictMarkers reports whether content contains a diff3 "ours" marker.
func HasConflictMarkers(content string) bool {
	for _, l := range strings.Split(content, "\n") {
		if reOurs.MatchString(l) {
			return true
		}
	}
	return false
}

// ParseConflicts extracts every diff3 conflict hunk from content, in order.
func ParseConflicts(content string) []ConflictHunk {
	lines := strings.Split(content, "\n")
	hunks := []ConflictHunk{}
	i := 0

	for i < len(lines) {
		m := reOurs.FindStringSubmatch(lines[i])
		if m == nil {
			i++
			continue
		}
		oursLabel := m[1]
		var ours, base, theirs []string
		theirsLabel := "theirs"
		startIdx := i
		i++

		for i < len(lines) && !reBase.MatchString(lines[i]) && !reSep.MatchString(lines[i]) {
			ours = append(ours, lines[i])
			i++
		}
		if i < len(lines) && reBase.MatchString(lines[i]) {
			i++
			for i < len(lines) && !reSep.MatchString(lines[i]) {
				base = append(base, lines[i])
				i++
			}
		}
		if i < len(lines) && reSep.MatchString(lines[i]) {
			i++
			for i < len(lines) && !reTheirs.MatchString(lines[i]) {
				theirs = append(theirs, lines[i])
				i++
			}
		}
		if i < len(lines) {
			if tm := reTheirs.FindStringSubmatch(lines[i]); tm != nil {
				theirsLabel = tm[1]
			}
			i++
		}

		contextBefore := append([]string{}, lines[max(0, startIdx-contextLines):startIdx]...)
		contextAfter := append([]string{}, lines[i:min(len(lines), i+contextLines)]...)

		hunks = append(hunks, ConflictHunk{
			OursLabel: oursLabel, TheirsLabel: theirsLabel,
			Ours: ours, Base: base, Theirs: theirs,
			ContextBefore: contextBefore, ContextAfter: contextAfter,
		})
	}

	return hunks
}

// ApplyResolutions replaces each conflict region in content with the resolved
// text, in order: resolutions[k] corresponds to the k-th hunk from
// ParseConflicts. A missing (out-of-range) or empty resolution drops the region.
func ApplyResolutions(content string, resolutions []string) string {
	lines := strings.Split(content, "\n")
	out := []string{}
	i := 0
	r := 0

	for i < len(lines) {
		if reOurs.MatchString(lines[i]) {
			// Skip the whole conflict block.
			for i < len(lines) && !reTheirs.MatchString(lines[i]) {
				i++
			}
			if i < len(lines) {
				i++ // consume the >>>>>>> line
			}
			resolved := ""
			if r < len(resolutions) {
				resolved = resolutions[r]
			}
			r++
			if len(resolved) > 0 {
				out = append(out, strings.Split(resolved, "\n")...)
			}
		} else {
			out = append(out, lines[i])
			i++
		}
	}

	return strings.Join(out, "\n")
}

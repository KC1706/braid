// Package contract is Braid's Rung 0 (contract-first) and Rung 5 (metamorphic
// contract preservation) — the two correctness rungs that bracket the merge
// (GO_PRODUCTION_PLAN.md §3 Phase 2 2b/2c):
//
//   - Rung 0 (this file): each subtask/agent carries a machine-readable
//     acceptance spec — a set of named Go tests that MUST exist before the code
//     and pass after it. The spec is frozen and content-hashed before code is
//     written, so it cannot later be edited to match whatever the code happened
//     to do. "The feature's test exists before its code."
//   - Rung 5 (metamorphic.go): snapshot each agent's observable behavior (its
//     contract checks) on its own fork, then assert the MERGED system still
//     satisfies every agent's contract — "both features survived". This catches
//     a merge that silently dropped or weakened a feature even when the build is
//     clean and whatever tests remain in the tree are green.
//
// The package is pure (determinism + stdlib only): it owns no git or process
// I/O. The merge engine, which already owns gitx, does the branch checkouts and
// hands this package a repoDir plus a CheckRunner seam.
package contract

import (
	"regexp"
	"sort"
	"strings"

	"github.com/KC1706/braid/internal/determinism"
)

// Check is one acceptance criterion: a named Go test that pins an agent's
// observable behavior. Package is a `go test` package pattern (e.g.
// "./internal/users" or "./..."); Test is the exact top-level test function name
// (used as the anchored run filter `^Test$`). Name is a short human label.
type Check struct {
	Name    string `json:"name"`
	Package string `json:"package"`
	Test    string `json:"test"`
}

// Contract is one agent/subtask's machine-readable acceptance spec: who it is,
// what it intends, and the checks that define "done". It is the Rung-0 artifact,
// authored and frozen before the agent writes code.
type Contract struct {
	Agent  string  `json:"agent"`
	Intent string  `json:"intent"`
	Checks []Check `json:"checks"`
}

// canonicalChecks returns the checks ordered deterministically (by Package, then
// Test, then Name) so a contract's hash does not depend on authoring order.
func (c Contract) canonicalChecks() []Check {
	out := make([]Check, len(c.Checks))
	copy(out, c.Checks)
	sort.Slice(out, func(i, j int) bool {
		if out[i].Package != out[j].Package {
			return out[i].Package < out[j].Package
		}
		if out[i].Test != out[j].Test {
			return out[i].Test < out[j].Test
		}
		return out[i].Name < out[j].Name
	})
	return out
}

// Hash is the canonical content hash of the spec: the agent/intent plus every
// check (package, test, name), each length-prefixed via determinism.ContentHash
// so the part boundaries are unambiguous. It is order-independent over the
// checks, so re-ordering the slice does not change the hash — only a genuine
// change to WHAT is asserted does.
func (c Contract) Hash() string {
	parts := []string{"agent", c.Agent, "intent", c.Intent}
	for _, chk := range c.canonicalChecks() {
		parts = append(parts, "check", chk.Package, chk.Test, chk.Name)
	}
	return determinism.ContentHash(parts...)
}

// Frozen is a contract pinned at freeze time: the spec plus the hash recorded
// before any code was written. Freezing is what makes Rung 0 meaningful — the
// recorded SpecHash is the commitment, and Verify rejects any later edit that
// would bend the spec to the code.
type Frozen struct {
	Contract Contract `json:"contract"`
	SpecHash string   `json:"specHash"`
	FrozenAt string   `json:"frozenAt"`
}

// Freeze records the contract's content hash at the given (caller-supplied, for
// determinism and testability) timestamp. The returned Frozen is the artifact to
// persist alongside the subtask before code generation begins.
func Freeze(c Contract, at string) Frozen {
	return Frozen{Contract: c, SpecHash: c.Hash(), FrozenAt: at}
}

// ErrSpecTampered is returned by Verify when the embedded contract no longer
// hashes to the recorded SpecHash — i.e. the spec was edited after it was frozen.
var ErrSpecTampered = errSpecTampered{}

type errSpecTampered struct{}

func (errSpecTampered) Error() string {
	return "contract: spec hash mismatch (spec was changed after it was frozen)"
}

// Verify reports whether the frozen contract is intact: it re-derives the hash
// from the embedded spec and compares it to the recorded SpecHash. A mismatch
// means the acceptance spec was altered after freezing (the Rung-0 violation).
func (f Frozen) Verify() error {
	if f.Contract.Hash() != f.SpecHash {
		return ErrSpecTampered
	}
	return nil
}

// reTestFuncName matches a top-level `func TestXxx(` (or Benchmark/Fuzz) and
// captures the name, so MissingTests can confirm a check's test exists in source
// before any code is written. It intentionally mirrors ladder.CountTestFuncs'
// regex shape (works even on a tree that no longer parses).
var reTestFuncName = regexp.MustCompile(`(?m)^func\s+((?:Test|Benchmark|Fuzz)\w*)\s*\(`)

// declaredTests returns the set of top-level test function names across the given
// test sources.
func declaredTests(testSources []string) map[string]bool {
	seen := map[string]bool{}
	for _, src := range testSources {
		for _, m := range reTestFuncName.FindAllStringSubmatch(src, -1) {
			seen[m[1]] = true
		}
	}
	return seen
}

// MissingTests returns, sorted, the Test names of every check whose test function
// is absent from the supplied test sources. An empty result means the contract's
// tests all exist — the Rung-0 "test before code" precondition is met. A check
// with an empty Test name is skipped (nothing to assert statically).
func (c Contract) MissingTests(testSources []string) []string {
	have := declaredTests(testSources)
	var missing []string
	for _, chk := range c.Checks {
		name := strings.TrimSpace(chk.Test)
		if name == "" {
			continue
		}
		if !have[name] {
			missing = append(missing, name)
		}
	}
	sort.Strings(missing)
	return missing
}

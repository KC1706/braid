// Package determinism holds the primitives that make a Braid run reproducible
// (GO_PRODUCTION_PLAN.md §3 Phase 2 / §4 2d): one content-hash primitive, a
// canonical (input-order-independent) ordering of the agents to fold, and a tree
// hash for asserting that identical inputs produce identical output.
//
// The reproducibility goal is: the same set of agent branches, folded under the
// same resolver, yields the same merged tree — regardless of the order the agents
// happened to arrive in, and (for the LLM path, via the resolver's cache keyed
// off ContentHash) regardless of model nondeterminism.
package determinism

import (
	"crypto/sha256"
	"encoding/hex"
	"sort"
	"strconv"
)

// ContentHash is the shared hashing primitive: hex SHA-256 over the parts, each
// length-prefixed so the boundary between parts is unambiguous (so ("ab","c")
// and ("a","bc") hash differently). Agent ordering, the resolution cache key and
// any reproducibility check all go through this, so they agree on what "the same
// input" means.
func ContentHash(parts ...string) string {
	h := sha256.New()
	for _, p := range parts {
		h.Write([]byte(strconv.Itoa(len(p))))
		h.Write([]byte{':'})
		h.Write([]byte(p))
	}
	return hex.EncodeToString(h.Sum(nil))
}

// CanonicalOrder returns a copy of items reordered into a deterministic,
// input-order-independent sequence: ascending by ContentHash(key(item)), with
// key(item) itself as a stable tiebreaker should two keys ever collide. Folding
// the same set of agents in this canonical order makes the merged result
// independent of the order the caller supplied them — a precondition for a
// reproducible output hash across runs where agents finish in a racy order.
func CanonicalOrder[T any](items []T, key func(T) string) []T {
	out := make([]T, len(items))
	copy(out, items)
	keys := make(map[int]string, len(out))
	hashes := make(map[int]string, len(out))
	for i := range out {
		k := key(out[i])
		keys[i] = k
		hashes[i] = ContentHash(k)
	}
	// Sort indices to keep the key/hash lookups stable across swaps.
	idx := make([]int, len(out))
	for i := range idx {
		idx[i] = i
	}
	sort.SliceStable(idx, func(a, b int) bool {
		ia, ib := idx[a], idx[b]
		if hashes[ia] != hashes[ib] {
			return hashes[ia] < hashes[ib]
		}
		return keys[ia] < keys[ib]
	})
	sorted := make([]T, len(out))
	for i, j := range idx {
		sorted[i] = out[j]
	}
	return sorted
}

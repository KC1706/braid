// Package treemerge plans the pairwise reduction tree Braid folds N agents
// through (GO_PRODUCTION_PLAN.md §3 Phase 2 / §4 2e). Instead of a linear fold
// (((a0⊕a1)⊕a2)⊕a3) of depth O(N), the agents are folded as a balanced binary
// tree merge(merge(a0,a1), merge(a2,a3)) of depth O(log N). Each internal node is
// an independent merge of two sibling subtrees — a self-contained job that can run
// in its own sandbox/worktree — and the full test suite runs once at the root.
//
// This package is pure (no git, no I/O): it only decides the SHAPE of the fold,
// which the merge package then executes. The shape is deterministic, so combined
// with a canonical agent order the whole tree merge is reproducible.
package treemerge

// Node is one node of the reduction tree. A leaf folds a single agent (Leaf is
// its index into the canonically-ordered agent slice, and Left/Right are nil). An
// internal node merges its two children; its Leaf is -1.
type Node struct {
	Leaf  int // agent index for a leaf, -1 for an internal node
	Left  *Node
	Right *Node
}

// IsLeaf reports whether n folds a single agent (no children).
func (n *Node) IsLeaf() bool { return n.Left == nil && n.Right == nil }

// Depth returns the height of the tree rooted at n (a leaf has depth 0).
func (n *Node) Depth() int {
	if n == nil || n.IsLeaf() {
		return 0
	}
	l, r := n.Left.Depth(), n.Right.Depth()
	if l > r {
		return l + 1
	}
	return r + 1
}

// Leaves returns the agent indices under n, left to right (in-order over leaves).
func (n *Node) Leaves() []int {
	if n == nil {
		return nil
	}
	if n.IsLeaf() {
		return []int{n.Leaf}
	}
	return append(n.Left.Leaves(), n.Right.Leaves()...)
}

// Plan builds the balanced pairwise reduction tree over n leaves (agents 0..n-1
// in canonical order). It pairs adjacent nodes level by level, carrying an odd
// node straight up to the next level, so the tree is balanced and its depth is
// ceil(log2 n). Returns nil for n <= 0. The leaf order (and therefore the tree)
// is fully determined by n, so Plan is deterministic.
func Plan(n int) *Node {
	if n <= 0 {
		return nil
	}
	level := make([]*Node, n)
	for i := range level {
		level[i] = &Node{Leaf: i}
	}
	for len(level) > 1 {
		next := make([]*Node, 0, (len(level)+1)/2)
		for i := 0; i < len(level); i += 2 {
			if i+1 < len(level) {
				next = append(next, &Node{Leaf: -1, Left: level[i], Right: level[i+1]})
			} else {
				next = append(next, level[i]) // carry the odd node up a level
			}
		}
		level = next
	}
	return level[0]
}

package treemerge

import (
	"reflect"
	"sort"
	"testing"
)

func TestPlanDepthIsLogN(t *testing.T) {
	// depth = ceil(log2 n): the whole point of the tree fold vs a linear O(n).
	cases := map[int]int{1: 0, 2: 1, 3: 2, 4: 2, 5: 3, 7: 3, 8: 3, 9: 4, 16: 4, 17: 5}
	for n, wantDepth := range cases {
		if got := Plan(n).Depth(); got != wantDepth {
			t.Errorf("Plan(%d).Depth() = %d, want %d", n, got, wantDepth)
		}
	}
}

func TestPlanCoversEveryAgentOnce(t *testing.T) {
	for n := 1; n <= 20; n++ {
		leaves := Plan(n).Leaves()
		if len(leaves) != n {
			t.Fatalf("Plan(%d) has %d leaves, want %d", n, len(leaves), n)
		}
		got := append([]int(nil), leaves...)
		sort.Ints(got)
		want := make([]int, n)
		for i := range want {
			want[i] = i
		}
		if !reflect.DeepEqual(got, want) {
			t.Errorf("Plan(%d) leaves = %v, want each of 0..%d exactly once", n, leaves, n-1)
		}
	}
}

func TestPlanLeafOrderPreserved(t *testing.T) {
	// Leaves must stay in canonical (left-to-right) order so the fold is
	// deterministic and union composition order is stable.
	for n := 1; n <= 20; n++ {
		leaves := Plan(n).Leaves()
		for i := 1; i < len(leaves); i++ {
			if leaves[i-1] >= leaves[i] {
				t.Fatalf("Plan(%d) leaf order not ascending: %v", n, leaves)
			}
		}
	}
}

func TestPlanEmpty(t *testing.T) {
	if Plan(0) != nil || Plan(-1) != nil {
		t.Error("Plan(n<=0) should be nil")
	}
}

func TestPlanThreeIsLinearShape(t *testing.T) {
	// For n=3 the balanced tree is merge(merge(0,1), 2) — the same associativity
	// as the linear fold, which is what lets the equivalence test assert an
	// identical merged tree for the 3-agent fixture.
	root := Plan(3)
	if root.IsLeaf() || root.Right == nil || !root.Right.IsLeaf() || root.Right.Leaf != 2 {
		t.Fatalf("Plan(3) right child should be leaf 2: %+v", root)
	}
	if root.Left == nil || root.Left.IsLeaf() {
		t.Fatalf("Plan(3) left child should be internal (merge of 0,1)")
	}
	if l := root.Left.Leaves(); !reflect.DeepEqual(l, []int{0, 1}) {
		t.Fatalf("Plan(3) left leaves = %v, want [0 1]", l)
	}
}

package determinism

import "testing"

func TestContentHashStableAndUnambiguous(t *testing.T) {
	if ContentHash("a", "b") != ContentHash("a", "b") {
		t.Fatal("ContentHash is not stable for equal input")
	}
	// Length-prefixing must make the part boundary matter.
	if ContentHash("ab", "c") == ContentHash("a", "bc") {
		t.Fatal("ContentHash is ambiguous across the part boundary")
	}
	if ContentHash("x") == ContentHash("y") {
		t.Fatal("distinct input hashed equal")
	}
}

func TestCanonicalOrderIsPermutationInvariant(t *testing.T) {
	id := func(s string) string { return s }
	a := CanonicalOrder([]string{"gamma", "alpha", "beta"}, id)
	b := CanonicalOrder([]string{"beta", "gamma", "alpha"}, id)
	c := CanonicalOrder([]string{"alpha", "beta", "gamma"}, id)

	if len(a) != 3 {
		t.Fatalf("lost elements: %v", a)
	}
	for i := range a {
		if a[i] != b[i] || a[i] != c[i] {
			t.Fatalf("order not permutation-invariant: %v / %v / %v", a, b, c)
		}
	}
}

func TestCanonicalOrderDoesNotMutateInput(t *testing.T) {
	in := []string{"c", "a", "b"}
	_ = CanonicalOrder(in, func(s string) string { return s })
	if in[0] != "c" || in[1] != "a" || in[2] != "b" {
		t.Fatalf("input slice was mutated: %v", in)
	}
}

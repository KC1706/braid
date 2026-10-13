package resolver

import (
	"context"
	"fmt"
	"testing"
)

// countingResolver returns a DIFFERENT answer every call (simulating a
// nondeterministic LLM) and records how many times it was invoked.
type countingResolver struct{ calls int }

func (c *countingResolver) Name() string { return "counting" }
func (c *countingResolver) Resolve(_ context.Context, _ ResolveInput) (ResolveOutput, error) {
	c.calls++
	return ResolveOutput{Content: fmt.Sprintf("answer-%d", c.calls), Strategy: "llm"}, nil
}
func (c *countingResolver) ModelDigest() string { return "test-model" }

func TestCachingResolverMemoizesIdenticalConflicts(t *testing.T) {
	inner := &countingResolver{}
	c := NewCachingResolver(inner, "v1")
	in := ResolveInput{Path: "users.go", Hunk: sampleHunk(t), OursIntent: "a", TheirsIntent: "b"}

	first, err := c.Resolve(context.Background(), in)
	if err != nil {
		t.Fatal(err)
	}
	// Second call with identical input must return the SAME bytes without a new
	// call to the (nondeterministic) inner resolver.
	second, err := c.Resolve(context.Background(), in)
	if err != nil {
		t.Fatal(err)
	}
	if first.Content != second.Content {
		t.Fatalf("cache returned different content: %q vs %q", first.Content, second.Content)
	}
	if inner.calls != 1 {
		t.Fatalf("inner resolver called %d times, want 1 (cache miss then hit)", inner.calls)
	}
}

func TestCachingResolverKeyVariesWithInput(t *testing.T) {
	c := NewCachingResolver(&countingResolver{}, "v1")
	base := ResolveInput{Path: "users.go", Hunk: sampleHunk(t), OursIntent: "a", TheirsIntent: "b"}

	k := c.Key(base)

	// Each field that can change the answer must change the key.
	changed := []ResolveInput{
		func() ResolveInput { x := base; x.Path = "other.go"; return x }(),
		func() ResolveInput { x := base; x.OursIntent = "z"; return x }(),
		func() ResolveInput { x := base; x.TheirsIntent = "z"; return x }(),
		func() ResolveInput { x := base; x.LastFailure = "boom"; return x }(),
		func() ResolveInput {
			x := base
			h := x.Hunk
			h.Ours = []string{"different"}
			x.Hunk = h
			return x
		}(),
	}
	for i, x := range changed {
		if c.Key(x) == k {
			t.Errorf("case %d: key did not change when a keyed field changed", i)
		}
	}

	// Model digest and prompt version are part of the key.
	if NewCachingResolver(&countingResolver{}, "v2").Key(base) == k {
		t.Error("prompt version is not part of the key")
	}
}

func TestModelDigestOf(t *testing.T) {
	if got := ModelDigestOf(UnionResolver{}); got != "none" {
		t.Errorf("union digest = %q, want none", got)
	}
	if got := ModelDigestOf(ClaudeResolver{Model: "claude-opus-4-8"}); got != "claude-opus-4-8" {
		t.Errorf("claude digest = %q", got)
	}
}

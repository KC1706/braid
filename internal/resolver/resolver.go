// Package resolver turns a conflict hunk into replacement text. It is the Go
// port of the TS integrator's resolver.ts: a deterministic 3-way union, an
// HTTP-backed Workers AI resolver and a direct Claude resolver, and a hybrid that
// tries union first and escalates to an LLM only on a retry. Correctness is never
// decided here — the ladder re-runs the real tests on whatever a resolver returns.
package resolver

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/KC1706/braid/internal/determinism"
	"github.com/KC1706/braid/pkg/models"
)

// Pinned identities for determinism (GO_PRODUCTION_PLAN.md §4 2d). The model
// digests go into the resolution cache key so a model swap invalidates the cache;
// PromptVersion must be bumped whenever a resolver's prompt template changes.
const (
	claudeDefaultModel = "claude-opus-4-8"
	workersAIModel     = "@cf/meta/llama-3.3-70b-instruct-fp8-fast"
	PromptVersion      = "v1"
)

// ResolveInput is one conflict handed to a resolver.
type ResolveInput struct {
	Path         string
	Hunk         models.ConflictHunk
	OursIntent   string // intent of the "ours" agent
	TheirsIntent string // intent of the "theirs" agent
	Attempt      int    // retry counter; >0 means a previous resolution broke the tests
	LastFailure  string // test failure text from the previous attempt, for a better retry
}

// ResolveOutput is a resolver's proposed replacement for the whole conflict region.
type ResolveOutput struct {
	Content   string
	Rationale string
	Strategy  string // "union" | "llm"
}

// Resolver composes a conflict hunk into replacement text.
type Resolver interface {
	Name() string
	Resolve(ctx context.Context, in ResolveInput) (ResolveOutput, error)
}

// OptionalResolver is a resolver that may be unconfigured (no key / no endpoint).
type OptionalResolver interface {
	Resolver
	Available() bool
}

// added returns the lines one side inserted relative to the common ancestor,
// order preserved (a multiset difference so duplicated base lines are respected).
func added(side, base []string) []string {
	baseCount := map[string]int{}
	for _, l := range base {
		baseCount[l]++
	}
	var out []string
	for _, l := range side {
		if baseCount[l] > 0 {
			baseCount[l]--
		} else {
			out = append(out, l)
		}
	}
	return out
}

// UnionResolver is the deterministic 3-way union: keep BOTH agents' additions
// (in agent order) around the preserved base region. Resolves the canonical "two
// agents added a guard/call before the same statement" collision with no LLM, no
// network, no nondeterminism — the Spork/Mergiraf philosophy.
type UnionResolver struct{}

func (UnionResolver) Name() string { return "union" }

func (UnionResolver) Resolve(_ context.Context, in ResolveInput) (ResolveOutput, error) {
	oursAdded := added(in.Hunk.Ours, in.Hunk.Base)
	theirsAdded := added(in.Hunk.Theirs, in.Hunk.Base)
	parts := append(append(append([]string{}, oursAdded...), theirsAdded...), in.Hunk.Base...)
	return ResolveOutput{
		Content:  strings.Join(parts, "\n"),
		Strategy: "union",
		Rationale: fmt.Sprintf(
			"kept both; applied %q then %q around the shared line(s). independent concerns, so both additions survive.",
			in.OursIntent, in.TheirsIntent),
	}, nil
}

// httpClient is the shared client for the HTTP-backed resolvers.
var httpClient = &http.Client{Timeout: 60 * time.Second}

// WorkersAIResolver resolves a true conflict via Cloudflare Workers AI, called
// through the control-plane Worker's /api/resolve endpoint (the AI binding only
// exists inside a Worker). No Anthropic key needed.
type WorkersAIResolver struct {
	Endpoint string // full URL to POST, or "" when unconfigured
	Model    string // pinned model id, recorded as the digest for determinism
}

// NewWorkersAIResolver builds the resolver from workerURL (falls back to
// BRAID_WORKER_URL). An empty base leaves it unavailable.
func NewWorkersAIResolver(workerURL string) WorkersAIResolver {
	base := workerURL
	if base == "" {
		base = os.Getenv("BRAID_WORKER_URL")
	}
	ep := ""
	if base != "" {
		ep = strings.TrimRight(base, "/") + "/api/resolve"
	}
	return WorkersAIResolver{Endpoint: ep, Model: workersAIModel}
}

func (WorkersAIResolver) Name() string      { return "workers-ai" }
func (w WorkersAIResolver) Available() bool { return w.Endpoint != "" }

// ModelDigest pins the model identity into the resolution cache key.
func (w WorkersAIResolver) ModelDigest() string { return w.Model }

func (w WorkersAIResolver) Resolve(ctx context.Context, in ResolveInput) (ResolveOutput, error) {
	if !w.Available() {
		return ResolveOutput{}, fmt.Errorf("WorkersAIResolver: no BRAID_WORKER_URL configured")
	}
	body, _ := json.Marshal(map[string]any{
		"path":          in.Path,
		"oursIntent":    in.OursIntent,
		"theirsIntent":  in.TheirsIntent,
		"base":          strings.Join(in.Hunk.Base, "\n"),
		"ours":          strings.Join(in.Hunk.Ours, "\n"),
		"theirs":        strings.Join(in.Hunk.Theirs, "\n"),
		"contextBefore": strings.Join(in.Hunk.ContextBefore, "\n"),
		"contextAfter":  strings.Join(in.Hunk.ContextAfter, "\n"),
		"lastFailure":   in.LastFailure,
		// Pin sampling to 0 so the worker's model is as deterministic as it can be.
		"temperature": 0,
	})
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, w.Endpoint, bytes.NewReader(body))
	if err != nil {
		return ResolveOutput{}, err
	}
	req.Header.Set("content-type", "application/json")
	res, err := httpClient.Do(req)
	if err != nil {
		return ResolveOutput{}, err
	}
	defer res.Body.Close()
	raw, _ := io.ReadAll(res.Body)
	if res.StatusCode != http.StatusOK {
		return ResolveOutput{}, fmt.Errorf("Workers AI resolve %d: %s", res.StatusCode, string(raw))
	}
	var data struct {
		Content string `json:"content"`
	}
	if err := json.Unmarshal(raw, &data); err != nil {
		return ResolveOutput{}, err
	}
	return ResolveOutput{
		Content:  data.Content,
		Strategy: "llm",
		Rationale: fmt.Sprintf("Workers AI composed %q with %q (re-verified by tests).",
			in.OursIntent, in.TheirsIntent),
	}, nil
}

// ClaudeResolver resolves a true conflict with Claude (via Cloudflare AI Gateway
// when CF_AI_GATEWAY is set, else the Anthropic API directly).
type ClaudeResolver struct {
	APIKey   string
	Model    string
	Endpoint string
}

// NewClaudeResolver builds the resolver from the environment (ANTHROPIC_API_KEY,
// BRAID_MODEL, CF_AI_GATEWAY) with sensible defaults.
func NewClaudeResolver() ClaudeResolver {
	model := os.Getenv("BRAID_MODEL")
	if model == "" {
		model = claudeDefaultModel
	}
	endpoint := "https://api.anthropic.com/v1/messages"
	if gw := os.Getenv("CF_AI_GATEWAY"); gw != "" {
		endpoint = strings.TrimRight(gw, "/") + "/v1/messages"
	}
	return ClaudeResolver{APIKey: os.Getenv("ANTHROPIC_API_KEY"), Model: model, Endpoint: endpoint}
}

func (ClaudeResolver) Name() string      { return "claude" }
func (c ClaudeResolver) Available() bool { return c.APIKey != "" }

// ModelDigest pins the model identity into the resolution cache key.
func (c ClaudeResolver) ModelDigest() string { return c.Model }

func (c ClaudeResolver) Resolve(ctx context.Context, in ResolveInput) (ResolveOutput, error) {
	if !c.Available() {
		return ResolveOutput{}, fmt.Errorf("ClaudeResolver: no ANTHROPIC_API_KEY configured")
	}
	prompt := c.buildPrompt(in)
	body, _ := json.Marshal(map[string]any{
		"model":       c.Model,
		"max_tokens":  1024,
		"temperature": 0, // deterministic sampling (GO_PRODUCTION_PLAN.md §4 2d)
		"messages":    []map[string]string{{"role": "user", "content": prompt}},
	})
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.Endpoint, bytes.NewReader(body))
	if err != nil {
		return ResolveOutput{}, err
	}
	req.Header.Set("content-type", "application/json")
	req.Header.Set("x-api-key", c.APIKey)
	req.Header.Set("anthropic-version", "2023-06-01")
	res, err := httpClient.Do(req)
	if err != nil {
		return ResolveOutput{}, err
	}
	defer res.Body.Close()
	raw, _ := io.ReadAll(res.Body)
	if res.StatusCode != http.StatusOK {
		return ResolveOutput{}, fmt.Errorf("Claude API %d: %s", res.StatusCode, string(raw))
	}
	var data struct {
		Content []struct {
			Text string `json:"text"`
		} `json:"content"`
	}
	if err := json.Unmarshal(raw, &data); err != nil {
		return ResolveOutput{}, err
	}
	var sb strings.Builder
	for _, b := range data.Content {
		sb.WriteString(b.Text)
	}
	return ResolveOutput{
		Content:  stripFences(strings.TrimSpace(sb.String())),
		Strategy: "llm",
		Rationale: fmt.Sprintf("Claude composed %q with %q (re-verified by tests).",
			in.OursIntent, in.TheirsIntent),
	}, nil
}

func (c ClaudeResolver) buildPrompt(in ResolveInput) string {
	h := in.Hunk
	var b strings.Builder
	fmt.Fprintf(&b, "You are resolving a git merge conflict in `%s`.\n", in.Path)
	b.WriteString("Two agents edited the same region of one codebase:\n")
	fmt.Fprintf(&b, "- \"ours\" (%s) implements: %s\n", h.OursLabel, in.OursIntent)
	fmt.Fprintf(&b, "- \"theirs\" (%s) implements: %s\n\n", h.TheirsLabel, in.TheirsIntent)
	block := func(label string, lines []string) {
		fmt.Fprintf(&b, "%s:\n```\n%s\n```\n", label, strings.Join(lines, "\n"))
	}
	block("Common ancestor (base)", h.Base)
	block("Ours", h.Ours)
	block("Theirs", h.Theirs)
	block("Context immediately before", h.ContextBefore)
	block("Context immediately after", h.ContextAfter)
	if in.Attempt > 0 && in.LastFailure != "" {
		fmt.Fprintf(&b, "\nYour previous resolution FAILED the test suite:\n%s\nFix it.\n", in.LastFailure)
	}
	b.WriteString("\nCompose BOTH agents' intent into one correct version. Preserve indentation.\n")
	b.WriteString("Return ONLY the replacement code for the conflicted region — no markers, no fences, no commentary.")
	return b.String()
}

// stripFences removes a leading ```lang fence and a trailing ``` if the model
// wrapped its answer despite instructions.
func stripFences(s string) string {
	if strings.HasPrefix(s, "```") {
		if i := strings.IndexByte(s, '\n'); i >= 0 {
			s = s[i+1:]
		}
	}
	s = strings.TrimSuffix(strings.TrimRight(s, "\n"), "```")
	return strings.TrimRight(s, "\n")
}

// HybridResolver tries the deterministic union first and escalates to the LLM
// only on a retry (union broke the tests) when an LLM is available. Free and
// deterministic where possible; LLM only at a true overlap the cheap path missed.
type HybridResolver struct {
	Union UnionResolver
	LLM   OptionalResolver // may be nil
}

func (HybridResolver) Name() string { return "hybrid" }

func (h HybridResolver) Resolve(ctx context.Context, in ResolveInput) (ResolveOutput, error) {
	if in.Attempt == 0 || h.LLM == nil || !h.LLM.Available() {
		return h.Union.Resolve(ctx, in)
	}
	return h.LLM.Resolve(ctx, in)
}

// digester is implemented by resolvers that pin a model identity (the LLM ones).
type digester interface{ ModelDigest() string }

// ModelDigestOf returns a resolver's pinned model identity, or "none" for a
// purely deterministic resolver (the union) that has nothing to pin.
func ModelDigestOf(r Resolver) string {
	if d, ok := r.(digester); ok {
		return d.ModelDigest()
	}
	return "none"
}

// CachingResolver memoizes an inner resolver by the content of the conflict it is
// handed (GO_PRODUCTION_PLAN.md §4 2d). The key is
// hash(path+base+ours+theirs+contextBefore+contextAfter+oursIntent+theirsIntent+
// lastFailure+modelDigest+promptVersion) — every input that can change the
// answer. Identical conflicts under the same model+prompt therefore always
// resolve to the same bytes, which (a) makes an otherwise nondeterministic LLM
// resolver reproducible within and across runs, and (b) avoids a duplicate call
// for a repeated hunk. The union path is already deterministic; wrapping it is
// harmless. Safe for concurrent use.
type CachingResolver struct {
	Inner     Resolver
	PromptVer string

	mu    sync.Mutex
	cache map[string]ResolveOutput
}

// NewCachingResolver wraps inner. PromptVer defaults to the package PromptVersion.
func NewCachingResolver(inner Resolver, promptVer string) *CachingResolver {
	if promptVer == "" {
		promptVer = PromptVersion
	}
	return &CachingResolver{Inner: inner, PromptVer: promptVer, cache: map[string]ResolveOutput{}}
}

func (c *CachingResolver) Name() string { return "cache(" + c.Inner.Name() + ")" }

// Key is the deterministic cache key for one conflict (exported for tests and the
// provenance ledger, which records the key alongside the resolution).
func (c *CachingResolver) Key(in ResolveInput) string {
	h := in.Hunk
	return determinism.ContentHash(
		in.Path,
		strings.Join(h.Base, "\n"),
		strings.Join(h.Ours, "\n"),
		strings.Join(h.Theirs, "\n"),
		strings.Join(h.ContextBefore, "\n"),
		strings.Join(h.ContextAfter, "\n"),
		in.OursIntent,
		in.TheirsIntent,
		in.LastFailure,
		ModelDigestOf(c.Inner),
		c.PromptVer,
	)
}

func (c *CachingResolver) Resolve(ctx context.Context, in ResolveInput) (ResolveOutput, error) {
	key := c.Key(in)
	c.mu.Lock()
	if out, ok := c.cache[key]; ok {
		c.mu.Unlock()
		return out, nil
	}
	c.mu.Unlock()

	out, err := c.Inner.Resolve(ctx, in)
	if err != nil {
		return out, err
	}
	c.mu.Lock()
	c.cache[key] = out
	c.mu.Unlock()
	return out, nil
}

// AutoLLMResolver picks the LLM resolver by what's configured: Claude if
// ANTHROPIC_API_KEY is set, else Workers AI if BRAID_WORKER_URL is set, else nil
// (union-only). The bool reports whether one was found.
func AutoLLMResolver() (OptionalResolver, bool) {
	if c := NewClaudeResolver(); c.Available() {
		return c, true
	}
	if w := NewWorkersAIResolver(""); w.Available() {
		return w, true
	}
	return nil, false
}

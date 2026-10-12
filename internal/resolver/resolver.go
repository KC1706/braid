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
	"time"

	"github.com/KC1706/braid/pkg/models"
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
	return WorkersAIResolver{Endpoint: ep}
}

func (WorkersAIResolver) Name() string      { return "workers-ai" }
func (w WorkersAIResolver) Available() bool { return w.Endpoint != "" }

func (w WorkersAIResolver) Resolve(ctx context.Context, in ResolveInput) (ResolveOutput, error) {
	if !w.Available() {
		return ResolveOutput{}, fmt.Errorf("WorkersAIResolver: no BRAID_WORKER_URL configured")
	}
	body, _ := json.Marshal(map[string]string{
		"path":          in.Path,
		"oursIntent":    in.OursIntent,
		"theirsIntent":  in.TheirsIntent,
		"base":          strings.Join(in.Hunk.Base, "\n"),
		"ours":          strings.Join(in.Hunk.Ours, "\n"),
		"theirs":        strings.Join(in.Hunk.Theirs, "\n"),
		"contextBefore": strings.Join(in.Hunk.ContextBefore, "\n"),
		"contextAfter":  strings.Join(in.Hunk.ContextAfter, "\n"),
		"lastFailure":   in.LastFailure,
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
		model = "claude-opus-4-8"
	}
	endpoint := "https://api.anthropic.com/v1/messages"
	if gw := os.Getenv("CF_AI_GATEWAY"); gw != "" {
		endpoint = strings.TrimRight(gw, "/") + "/v1/messages"
	}
	return ClaudeResolver{APIKey: os.Getenv("ANTHROPIC_API_KEY"), Model: model, Endpoint: endpoint}
}

func (ClaudeResolver) Name() string      { return "claude" }
func (c ClaudeResolver) Available() bool { return c.APIKey != "" }

func (c ClaudeResolver) Resolve(ctx context.Context, in ResolveInput) (ResolveOutput, error) {
	if !c.Available() {
		return ResolveOutput{}, fmt.Errorf("ClaudeResolver: no ANTHROPIC_API_KEY configured")
	}
	prompt := c.buildPrompt(in)
	body, _ := json.Marshal(map[string]any{
		"model":      c.Model,
		"max_tokens": 1024,
		"messages":   []map[string]string{{"role": "user", "content": prompt}},
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

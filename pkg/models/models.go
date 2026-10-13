// Package models holds Braid's shared types with no logic: the verification
// verdict, the per-rung and ladder results, the provenance trail, and the
// dashboard event. It is the Go port of the TypeScript integrator's trail.ts,
// ladder.ts (the Verdict/RungResult/LadderResult types), and events.ts.
//
// The JSON tags deliberately mirror the TS field names (camelCase) so a
// .braid/trail.json written by the Go core deserializes in the existing TS edge
// and dashboard unchanged — behaviour parity first (GO_MIGRATION_PLAN.md §7).
package models

// Verdict is the structured outcome of a verification-ladder run. It replaces an
// exit-code boolean: "inconclusive" is never silently treated as green, and
// "incompatible" is reserved for the §5.1 incompatibility path.
type Verdict string

const (
	VerdictGreen        Verdict = "green"        // every rung that ran passed
	VerdictRed          Verdict = "red"          // a rung failed (tests red, or a guard rejected the merge)
	VerdictInconclusive Verdict = "inconclusive" // could not decide (flaky / infra) — never treated as green
	VerdictIncompatible Verdict = "incompatible" // the candidates cannot be composed (§5.1 path)
)

// RungResult is the outcome of a single ladder rung. Detail carries failure
// diagnostics that a resolver retry can feed on; it is empty on success.
type RungResult struct {
	Rung    int    `json:"rung"`
	Name    string `json:"name"`
	Passed  bool   `json:"passed"`
	Summary string `json:"summary"`          // short line for the trail / dashboard
	Detail  string `json:"detail,omitempty"` // failure detail, fed back into a resolver retry
}

// LadderResult is the outcome of running the whole ladder. Passed is the
// convenience bool (Verdict == VerdictGreen); Output is the deciding rung's
// failure detail, for resolver-retry feedback.
type LadderResult struct {
	Verdict Verdict      `json:"verdict"`
	Passed  bool         `json:"passed"`
	Summary string       `json:"summary"`
	Output  string       `json:"output"`
	Rungs   []RungResult `json:"rungs"`
}

// ConflictHunk is one diff3 conflict region, shared data between the merge
// driver (which parses it) and the resolver (which composes it). The base region
// — present only with merge.conflictStyle=diff3 — is what lets Braid do a precise
// 3-way union instead of a blind pick.
type ConflictHunk struct {
	OursLabel     string
	TheirsLabel   string
	Ours          []string
	Base          []string
	Theirs        []string
	ContextBefore []string // a few lines immediately before the hunk, for resolver context
	ContextAfter  []string // a few lines immediately after the hunk
}

// Candidate is one agent's proposed content for a conflicted hunk.
type Candidate struct {
	Agent   string `json:"agent"`
	Intent  string `json:"intent"`
	Content string `json:"content"`
}

// ConflictRecord is the "why" record for one resolved conflict — Braid's answer
// to "track not just what changed, but why".
type ConflictRecord struct {
	Path       string      `json:"path"`
	Base       string      `json:"base"` // common-ancestor text for the hunk
	Candidates []Candidate `json:"candidates"`
	Resolution string      `json:"resolution"`
	Rationale  string      `json:"rationale"`
	Strategy   string      `json:"strategy"` // "union" | "llm"
	Verified   string      `json:"verified"`
	Attempts   int         `json:"attempts"`
}

// AgentMergeRecord is the per-agent outcome of the integration loop. Dropped is
// set when the incompatibility policy discarded this agent's work.
type AgentMergeRecord struct {
	Agent           string   `json:"agent"`
	Branch          string   `json:"branch"`
	Intent          string   `json:"intent"`
	AutoMergedFiles []string `json:"autoMergedFiles"`
	ConflictedFiles []string `json:"conflictedFiles"`
	TestResult      string   `json:"testResult"`
	Dropped         bool     `json:"dropped,omitempty"`
	DropReason      string   `json:"dropReason,omitempty"`
}

// IncompatibilityRecord is written when an agent could not be composed with the
// integrated set (§5.1, C4). It preserves BOTH candidates and the reason —
// nothing is silently lost — plus the policy's decision.
type IncompatibilityRecord struct {
	Agent       string   `json:"agent"`
	Intent      string   `json:"intent"`
	WithIntents []string `json:"withIntents"` // intents already integrated (the side it conflicts with)
	Reason      string   `json:"reason"`
	File        string   `json:"file,omitempty"`
	Detail      string   `json:"detail,omitempty"`  // tail of the verification failure that proved it
	Action      string   `json:"action"`            // "escalate" | "drop"
	Dropped     string   `json:"dropped,omitempty"` // for "drop": which agent was discarded
	Note        string   `json:"note"`
}

// VerificationRecord is the deciding ladder verdict for the final merged tree,
// recorded in the trail.
type VerificationRecord struct {
	Verdict Verdict      `json:"verdict"`
	Summary string       `json:"summary"`
	Rungs   []RungResult `json:"rungs"`
}

// TrailSummary is the headline count block at the foot of a trail.
type TrailSummary struct {
	AgentsMerged      int `json:"agentsMerged"`
	FilesAutoMerged   int `json:"filesAutoMerged"`
	ConflictsResolved int `json:"conflictsResolved"`
	LLMResolutions    int `json:"llmResolutions"`
}

// Trail is Braid's full provenance record for one integration run, written to
// .braid/trail.json.
type Trail struct {
	Task              string                  `json:"task"`
	CreatedAt         string                  `json:"createdAt"`
	Baseline          string                  `json:"baseline"`
	MergedSHA         string                  `json:"mergedSha"`
	Agents            []AgentMergeRecord      `json:"agents"`
	Conflicts         []ConflictRecord        `json:"conflicts"`
	Incompatibilities []IncompatibilityRecord `json:"incompatibilities,omitempty"`
	Verdict           Verdict                 `json:"verdict,omitempty"`
	FinalTest         string                  `json:"finalTest"`
	FinalVerification *VerificationRecord     `json:"finalVerification,omitempty"`
	Summary           TrailSummary            `json:"summary"`
}

// Event is one structured event streamed to the dashboard Durable Object (parity
// with the TS BraidEvent union). Type is the discriminator; the optional fields
// carry the payload for whichever variant Type names. Zero-valued fields are
// omitted so the wire shape stays close to the discriminated TS union.
type Event struct {
	Type string `json:"type"`

	// Seq is a per-run monotonic sequence number stamped by the HTTP emitter so a
	// consumer can detect a dropped event and request replay (C10). Zero when the
	// event was not sent through a sequencing emitter.
	Seq int `json:"seq,omitempty"`

	RunID string `json:"runId,omitempty"`
	Task  string `json:"task,omitempty"`
	Mode  string `json:"mode,omitempty"`

	Agent  string `json:"agent,omitempty"`
	Branch string `json:"branch,omitempty"`
	Intent string `json:"intent,omitempty"`
	Status string `json:"status,omitempty"`

	File     string `json:"file,omitempty"`
	Strategy string `json:"strategy,omitempty"`
	Attempt  int    `json:"attempt,omitempty"`

	Verdict Verdict      `json:"verdict,omitempty"`
	Rungs   []RungResult `json:"rungs,omitempty"`

	Message   string `json:"message,omitempty"`
	MergedSHA string `json:"mergedSha,omitempty"`
}

// Emitter delivers events to the dashboard, best-effort (an HTTP POST to the
// worker in the live path). Emit never blocks the merge engine on a failure;
// Flush waits for any in-flight deliveries.
type Emitter interface {
	Emit(Event)
	Flush() error
}

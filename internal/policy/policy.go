// Package policy implements Braid's incompatibility path (PRODUCTION_PLAN.md
// §5.1, fixes C4 — "union is not a law"). It is the Go port of the TS
// integrator's policy.ts.
//
// Braid composes by default, but some agents' changes are provably mutually
// exclusive: every attempted composition stays red, or a clean merge breaks
// verification in a way no resolution can fix. Forcing a merge there is nonsense
// even when it goes green. When that happens the engine does NOT crash — it
// records both candidates and hands the decision to a pluggable Policy. The
// default is EscalatePolicy (stop and surface, never silently discard an agent's
// work); PickWinnerPolicy is available for callers that want full autonomy.
package policy

import (
	"fmt"
	"math"
	"strings"
)

// AgentInput is the minimal identity of an agent branch the policy reasons over.
// It mirrors the merge engine's AgentInput; policy keeps its own copy to stay a
// leaf package (no import cycle with internal/merge).
type AgentInput struct {
	ID     string
	Branch string
	Intent string
}

// Context is what the engine hands the policy when an agent cannot be reconciled.
type Context struct {
	Agent            AgentInput   // the incoming agent whose branch could not be reconciled
	IntegratedAgents []AgentInput // agents already merged and verified (the "ours" side)
	File             string       // conflicted file, if this came from the conflict path (optional)
	Reason           string       // human-readable reason, e.g. "clean merge broke verification [red]"
	LastFailure      string       // verification/test failure detail from the last attempt (optional)
}

// Action is the kind of decision a policy can return.
type Action string

const (
	ActionEscalate Action = "escalate" // halt the run and surface both candidates; nothing dropped
	ActionDrop     Action = "drop"     // drop exactly one agent and continue integrating the rest
)

// Decision is a policy's verdict. Drop names the discarded agent id when
// Action == ActionDrop; it is empty for escalate.
type Decision struct {
	Action Action
	Drop   string
	Note   string
}

// Policy decides what to do when an agent is incompatible with the integrated
// set. Implementations must be deterministic.
type Policy interface {
	Name() string
	Decide(ctx Context) Decision
}

// EscalatePolicy is the default: never auto-drop. Stop the run with an
// incompatible verdict so a human (or a future arbiter agent) can decide. This
// keeps Braid's promise that it will not silently throw away an agent's work.
type EscalatePolicy struct{}

func (EscalatePolicy) Name() string { return "escalate" }

func (EscalatePolicy) Decide(ctx Context) Decision {
	integrated := agentIDs(ctx.IntegratedAgents)
	if integrated == "" {
		integrated = "baseline"
	}
	return Decision{
		Action: ActionEscalate,
		Note: fmt.Sprintf(
			"%s (%q) cannot be composed with [%s]; escalating for a human/arbiter decision (nothing dropped).",
			ctx.Agent.ID, ctx.Agent.Intent, integrated,
		),
	}
}

// PickWinnerPolicy is the opt-in autonomous policy: keep the higher-priority side
// and drop the loser, recording the discarded candidate. Priority is given by
// Order (earlier = higher priority); any agent not listed ranks after those
// listed. In this streaming integrator the already-integrated agents are the
// accumulated winner, so the incoming agent is the one dropped; when it outranks
// the integrated set we still drop it here (prior work is already committed and
// verified) but say so, leaving a true re-order to a batch re-run.
type PickWinnerPolicy struct {
	Order []string
}

func (PickWinnerPolicy) Name() string { return "pick-winner" }

// rank returns the priority index of id (lower = higher priority); an unlisted
// id ranks after every listed one.
func (p PickWinnerPolicy) rank(id string) int {
	for i, o := range p.Order {
		if o == id {
			return i
		}
	}
	return math.MaxInt
}

func (p PickWinnerPolicy) Decide(ctx Context) Decision {
	incomingRank := p.rank(ctx.Agent.ID)
	bestIntegrated := math.MaxInt
	for _, a := range ctx.IntegratedAgents {
		if r := p.rank(a.ID); r < bestIntegrated {
			bestIntegrated = r
		}
	}
	outranksIntegrated := incomingRank < bestIntegrated

	note := fmt.Sprintf("kept the integrated set; dropped lower-priority %s (%q).", ctx.Agent.ID, ctx.Agent.Intent)
	if outranksIntegrated {
		note = fmt.Sprintf(
			"%s outranks the integrated set by priority but prior work is already committed; "+
				"dropping %s this run — re-run with it ordered first to prefer it.",
			ctx.Agent.ID, ctx.Agent.ID,
		)
	}
	return Decision{Action: ActionDrop, Drop: ctx.Agent.ID, Note: note}
}

// agentIDs joins agent ids as "a, b, c" for human-readable notes.
func agentIDs(agents []AgentInput) string {
	ids := make([]string, len(agents))
	for i, a := range agents {
		ids[i] = a.ID
	}
	return strings.Join(ids, ", ")
}

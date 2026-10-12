import type { AgentInput } from "./integrate.js";

/**
 * The incompatibility path (PRODUCTION_PLAN.md §5.1, fixes C4 — "union is not a
 * law"). Braid composes by default, but some agents' changes are provably
 * mutually exclusive: every attempted composition stays red, or a clean merge
 * breaks verification in a way no resolution can fix. Forcing a merge there is
 * nonsense even when it goes green.
 *
 * When that happens the engine does NOT crash. It records both candidates and
 * hands the decision to a pluggable policy. The default is `escalate` — stop and
 * surface, never silently discard an agent's work (faithful to the "compose,
 * don't pick a winner" thesis). `pick-winner` is available for callers that want
 * full autonomy and accept dropping the losing candidate.
 */

export interface IncompatibilityContext {
  /** The incoming agent whose branch could not be reconciled. */
  agent: AgentInput;
  /** Agents already merged and verified (the "ours" side of this step). */
  integratedAgents: AgentInput[];
  /** File whose conflict could not be resolved, if this came from the conflict path. */
  file?: string;
  /** Human-readable reason (e.g. "clean merge broke verification [red]"). */
  reason: string;
  /** Verification/test failure detail from the last attempt. */
  lastFailure?: string;
}

export type IncompatibilityDecision =
  /** Halt the run and surface both candidates; nothing is dropped. */
  | { action: "escalate"; note: string }
  /** Drop exactly one agent (by id) and continue integrating the rest. */
  | { action: "drop"; drop: string; note: string };

export interface IncompatibilityPolicy {
  readonly name: string;
  decide(ctx: IncompatibilityContext): IncompatibilityDecision;
}

/**
 * Default policy: never auto-drop. Stop the run with an `incompatible` verdict so
 * a human (or a future arbiter agent) can decide. This keeps Braid's promise
 * that it will not silently throw away an agent's work.
 */
export class EscalatePolicy implements IncompatibilityPolicy {
  readonly name = "escalate";
  decide(ctx: IncompatibilityContext): IncompatibilityDecision {
    return {
      action: "escalate",
      note:
        `${ctx.agent.id} ("${ctx.agent.intent}") cannot be composed with ` +
        `[${ctx.integratedAgents.map((a) => a.id).join(", ") || "baseline"}]; ` +
        `escalating for a human/arbiter decision (nothing dropped).`,
    };
  }
}

/**
 * Opt-in autonomous policy: keep the higher-priority side and drop the loser,
 * recording the discarded candidate. Priority is given by `order` (earlier =
 * higher priority); any agent not listed ranks after those listed, in arrival
 * order. In this streaming integrator the already-integrated agents are the
 * accumulated winner, so when the incoming agent ranks no higher than every
 * integrated agent it is the one dropped; if it outranks the weakest integrated
 * agent we still drop the incoming one here (prior work is already committed and
 * verified) but say so in the note, leaving a true re-order to a batch re-run.
 */
export class PickWinnerPolicy implements IncompatibilityPolicy {
  readonly name = "pick-winner";
  constructor(private readonly order: string[] = []) {}

  private rank(id: string): number {
    const i = this.order.indexOf(id);
    return i === -1 ? Number.MAX_SAFE_INTEGER : i;
  }

  decide(ctx: IncompatibilityContext): IncompatibilityDecision {
    const incomingRank = this.rank(ctx.agent.id);
    const bestIntegrated = Math.min(
      Number.MAX_SAFE_INTEGER,
      ...ctx.integratedAgents.map((a) => this.rank(a.id)),
    );
    const outranksIntegrated = incomingRank < bestIntegrated;
    return {
      action: "drop",
      drop: ctx.agent.id,
      note: outranksIntegrated
        ? `${ctx.agent.id} outranks the integrated set by priority but prior work is ` +
          `already committed; dropping ${ctx.agent.id} this run — re-run with it ordered first to prefer it.`
        : `kept the integrated set; dropped lower-priority ${ctx.agent.id} ("${ctx.agent.intent}").`,
    };
  }
}

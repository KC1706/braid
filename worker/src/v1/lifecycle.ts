/**
 * Run lifecycle — the control plane's state machine (GO_PRODUCTION_PLAN.md §3
 * Phase 3b). A run is created `queued`, driven by the Workflow consumer through
 * `running`, and may pause at `awaiting_approval` when the engine escalates an
 * incompatibility for a human decision (the §5.1 path). Every transition is an
 * explicit, validated action, so an out-of-order or illegal request (e.g.
 * approving a run that already succeeded) is rejected rather than silently
 * applied. The machine is pure: no I/O, no clock — the caller stamps time and
 * persists the result (the DO in prod).
 */

export type RunStatus =
  | "queued"
  | "running"
  | "awaiting_approval"
  | "succeeded"
  | "failed"
  | "incompatible"
  | "canceled";

/** Actions that advance a run. Each maps to one allowed set of source states. */
export type RunAction =
  | "start" // queued -> running (the Workflow picked it up)
  | "escalate" // running -> awaiting_approval (incompatibility needs a human)
  | "approve" // awaiting_approval -> running (operator approved; resume)
  | "reject" // awaiting_approval -> incompatible (operator rejected)
  | "succeed" // running -> succeeded (green verdict)
  | "fail" // running -> failed (red verdict / infra)
  | "incompatible" // running -> incompatible (cannot be composed, no human override)
  | "cancel"; // queued|running|awaiting_approval -> canceled

/** The terminal states — a run in one of these accepts no further action. */
const TERMINAL: ReadonlySet<RunStatus> = new Set<RunStatus>([
  "succeeded",
  "failed",
  "incompatible",
  "canceled",
]);

/** allowedFrom[action] = the source states that action may be applied from. */
const allowedFrom: Record<RunAction, ReadonlySet<RunStatus>> = {
  start: new Set(["queued"]),
  escalate: new Set(["running"]),
  approve: new Set(["awaiting_approval"]),
  reject: new Set(["awaiting_approval"]),
  succeed: new Set(["running"]),
  fail: new Set(["running"]),
  incompatible: new Set(["running"]),
  cancel: new Set(["queued", "running", "awaiting_approval"]),
};

/** nextOf[action] = the resulting state. */
const nextOf: Record<RunAction, RunStatus> = {
  start: "running",
  escalate: "awaiting_approval",
  approve: "running",
  reject: "incompatible",
  succeed: "succeeded",
  fail: "failed",
  incompatible: "incompatible",
  cancel: "canceled",
};

export interface TransitionOk {
  ok: true;
  next: RunStatus;
}
export interface TransitionErr {
  ok: false;
  error: string;
}
export type TransitionResult = TransitionOk | TransitionErr;

/** isTerminal reports whether a run in `status` accepts no further action. */
export function isTerminal(status: RunStatus): boolean {
  return TERMINAL.has(status);
}

/**
 * transition validates `action` against `current` and returns the next state or
 * an error. It never throws, so a handler can map the error straight to a 409.
 */
export function transition(current: RunStatus, action: RunAction): TransitionResult {
  const from = allowedFrom[action];
  if (!from) return { ok: false, error: `unknown action "${action}"` };
  if (isTerminal(current)) {
    return { ok: false, error: `run is ${current} (terminal); "${action}" not allowed` };
  }
  if (!from.has(current)) {
    return { ok: false, error: `"${action}" not allowed from ${current}` };
  }
  return { ok: true, next: nextOf[action] };
}

/** One recorded step in a run's history, for the audit trail. */
export interface RunHistoryEntry {
  at: string; // RFC3339, stamped by the caller
  action: RunAction | "create";
  from: RunStatus | null;
  to: RunStatus;
  note?: string;
}

/** A run record as the control plane stores it. */
export interface Run {
  id: string;
  status: RunStatus;
  task: string;
  base: string;
  agents: string[];
  createdAt: string;
  updatedAt: string;
  /** Idempotency key the creating request carried, if any (for dedup lookups). */
  idempotencyKey?: string;
  /** Fingerprint of the creating request body, to detect key reuse with a different payload. */
  requestFingerprint?: string;
  history: RunHistoryEntry[];
  verdict?: string;
  mergedSha?: string;
}

export interface NewRunInput {
  id: string;
  task: string;
  base: string;
  agents: string[];
  at: string;
  idempotencyKey?: string;
  requestFingerprint?: string;
}

/** createRun builds a fresh `queued` run with its first history entry. */
export function createRun(input: NewRunInput): Run {
  return {
    id: input.id,
    status: "queued",
    task: input.task,
    base: input.base,
    agents: input.agents,
    createdAt: input.at,
    updatedAt: input.at,
    idempotencyKey: input.idempotencyKey,
    requestFingerprint: input.requestFingerprint,
    history: [{ at: input.at, action: "create", from: null, to: "queued" }],
  };
}

/**
 * applyAction returns a NEW run with the action applied, or an error. It appends
 * a history entry and bumps updatedAt; it never mutates the input (so a failed
 * transition leaves the stored run untouched).
 */
export function applyAction(
  run: Run,
  action: RunAction,
  at: string,
  note?: string,
): { ok: true; run: Run } | { ok: false; error: string } {
  const t = transition(run.status, action);
  if (!t.ok) return { ok: false, error: t.error };
  const entry: RunHistoryEntry = { at, action, from: run.status, to: t.next, note };
  return {
    ok: true,
    run: {
      ...run,
      status: t.next,
      updatedAt: at,
      history: [...run.history, entry],
    },
  };
}

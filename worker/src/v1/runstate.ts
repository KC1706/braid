/**
 * The Orchestrator's run-state projection (GO_PRODUCTION_PLAN.md §3 Phase 4c).
 * This is a PURE, DETERMINISTIC reducer over a sequence of (event, meta) pairs:
 * folding the same event log from the same start always yields the same state.
 * That determinism is what makes a run rebuildable — the DO persists each event
 * with a monotonic seq + receive timestamp, and `rebuild` replays them to
 * reconstruct the state (git stays the source of truth; the DO is an outbox,
 * C10/C11). No `Date.now()` here: time comes in through meta.ts.
 */

export type BraidEvent = Record<string, any> & { type: string };

/** Server-assigned metadata for one ingested event. */
export interface EventMeta {
  seq: number; // monotonic, 1-based
  ts: number; // receive time (ms), supplied by the DO; kept out of the reducer's own clock
}

/** One persisted log entry: the event plus its metadata. */
export interface LoggedEvent {
  meta: EventMeta;
  event: BraidEvent;
}

export interface AgentView {
  id: string;
  branch: string;
  intent: string;
  status: string; // spawned | forked | coding | pushed
  merge: string; // waiting | merging | auto-merged | conflict | resolved | verified
  files: string[];
  test: string;
}

export interface RunState {
  runId: string;
  task: string;
  mode: string;
  startedAt: number;
  updatedAt: number;
  /** Seq of the last event folded in — clients use it to detect gaps / resume. */
  seq: number;
  phase: string; // init | swarming | integrating | done
  base: { name: string; remote: string } | null;
  agents: Record<string, AgentView>;
  agentOrder: string[];
  conflicts: {
    agent: string;
    path: string;
    strategy: string;
    attempts: number;
    verified: string;
    status: string; // resolving | resolved
  }[];
  result: { green: boolean; finalTest: string; mergedSha: string; summary: Record<string, number>; usersFile?: string } | null;
  log: { ts: number; line: string }[];
}

const MAX_LOG = 200;

/** emptyState builds the zero state. startedAt comes from the caller (not a clock). */
export function emptyState(runId: string, startedAt: number): RunState {
  return {
    runId,
    task: "",
    mode: "",
    startedAt,
    updatedAt: startedAt,
    seq: 0,
    phase: "init",
    base: null,
    agents: {},
    agentOrder: [],
    conflicts: [],
    result: null,
    log: [],
  };
}

function pushLog(s: RunState, ts: number, line: string) {
  s.log.push({ ts, line });
  if (s.log.length > MAX_LOG) s.log.shift();
}

/**
 * reduce folds one event into the state, mutating + returning it. Deterministic:
 * every time-stamp and the seq come from `meta`, never from a clock, so replaying
 * the same (event, meta) log reproduces the state byte-for-byte.
 */
export function reduce(s: RunState, e: BraidEvent, meta: EventMeta): RunState {
  s.updatedAt = meta.ts;
  s.seq = meta.seq;
  const ts = meta.ts;
  switch (e.type) {
    case "run:init": {
      s.task = e.task;
      s.mode = e.mode;
      s.phase = "swarming";
      for (const a of e.agents as { id: string; branch: string; intent: string }[]) {
        s.agents[a.id] = { id: a.id, branch: a.branch, intent: a.intent, status: "spawned", merge: "waiting", files: [], test: "" };
        if (!s.agentOrder.includes(a.id)) s.agentOrder.push(a.id);
      }
      pushLog(s, ts, `run started — ${e.agents.length} agents · ${e.mode}`);
      break;
    }
    case "base:ready":
      s.base = { name: e.name, remote: e.remote };
      pushLog(s, ts, `base repo ready: ${e.name}`);
      break;
    case "agent:status": {
      const a = s.agents[e.agent];
      if (a) a.status = e.status;
      pushLog(s, ts, `${e.agent}: ${e.status}${e.detail ? ` — ${e.detail}` : ""}`);
      break;
    }
    case "merge:start": {
      s.phase = "integrating";
      const a = s.agents[e.agent];
      if (a) a.merge = "merging";
      // scope "all" (Phase 3c) = the whole fold; a named agent = the linear path.
      pushLog(s, ts, e.agent ? `integrating ${e.agent}…` : `integrating ${e.scope === "all" ? "all agents" : "merge"}…`);
      break;
    }
    case "merge:auto": {
      const a = s.agents[e.agent];
      if (a) {
        a.merge = "auto-merged";
        a.files = e.files;
      }
      pushLog(s, ts, `${e.agent}: git auto-merged ${e.files.length} file(s) — no AI`);
      break;
    }
    case "conflict:detected": {
      const a = s.agents[e.agent];
      if (a) a.merge = "conflict";
      for (const path of e.files as string[]) {
        if (!s.conflicts.find((c) => c.agent === e.agent && c.path === path)) {
          s.conflicts.push({ agent: e.agent, path, strategy: "?", attempts: 0, verified: "", status: "resolving" });
        }
      }
      pushLog(s, ts, `${e.agent}: ⚡ conflict in ${(e.files as string[]).join(", ")}`);
      break;
    }
    case "conflict:attempt": {
      const c = s.conflicts.find((c) => c.agent === e.agent && c.path === e.path);
      if (c) {
        c.strategy = e.strategy;
        c.attempts = e.attempt;
        c.status = "resolving";
      }
      pushLog(s, ts, `${e.agent}: resolving ${e.path} via ${e.strategy} (attempt ${e.attempt})`);
      break;
    }
    case "conflict:resolved": {
      const c = s.conflicts.find((c) => c.agent === e.agent && c.path === e.path);
      if (c) {
        c.strategy = e.strategy;
        c.attempts = e.attempts;
        c.verified = e.verified;
        c.status = "resolved";
      }
      const a = s.agents[e.agent];
      if (a) a.merge = "resolved";
      pushLog(s, ts, `${e.agent}: ✓ ${e.path} resolved via ${e.strategy} — ${e.verified}`);
      break;
    }
    case "verify": {
      if (e.scope !== "final") {
        const a = s.agents[e.scope];
        if (a) {
          a.test = e.summary;
          if (e.passed && a.merge !== "auto-merged") a.merge = "verified";
        }
      }
      pushLog(s, ts, `${e.scope === "final" ? "final" : e.scope} tests: ${e.passed ? "✅" : "❌"} ${e.summary}`);
      break;
    }
    case "run:done":
      s.phase = "done";
      s.result = { green: e.green, finalTest: e.finalTest, mergedSha: e.mergedSha, summary: e.summary, usersFile: e.usersFile };
      pushLog(s, ts, `run ${e.green ? "GREEN ✅" : "RED ❌"} — ${e.finalTest}`);
      break;
    case "log":
      pushLog(s, ts, e.line);
      break;
  }
  return s;
}

/**
 * rebuild reconstructs a run's state by folding its event log from a start state
 * (emptyState, or a snapshot). This is the proof of rebuildability: the DO's live
 * state must always equal rebuild(runId, its log). `from` lets a caller resume
 * from a snapshot and replay only the tail.
 */
export function rebuild(runId: string, log: LoggedEvent[], from?: RunState): RunState {
  const start = from ?? emptyState(runId, log.length ? log[0].meta.ts : 0);
  let s = start;
  for (const { event, meta } of log) {
    // Skip events already folded into the snapshot.
    if (meta.seq <= s.seq) continue;
    s = reduce(s, event, meta);
  }
  return s;
}

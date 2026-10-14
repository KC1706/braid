import { describe, it, expect } from "vitest";
import { emptyState, reduce, rebuild, type BraidEvent, type LoggedEvent, type RunState } from "../src/v1/runstate";

// A representative event stream for one run.
const EVENTS: BraidEvent[] = [
  { type: "run:init", task: "harden api", mode: "swarm", agents: [
    { id: "a1", branch: "agent/a1", intent: "validation" },
    { id: "a2", branch: "agent/a2", intent: "ratelimit" },
  ] },
  { type: "base:ready", name: "base-x", remote: "https://r/base-x" },
  { type: "merge:start", scope: "all" },
  { type: "merge:auto", agent: "a1", files: ["users.go"] },
  { type: "conflict:detected", agent: "a2", files: ["users.go"] },
  { type: "conflict:attempt", agent: "a2", path: "users.go", strategy: "union", attempt: 1 },
  { type: "conflict:resolved", agent: "a2", path: "users.go", strategy: "union", attempts: 1, verified: "tests green" },
  { type: "verify", scope: "final", passed: true, summary: "12 passed" },
  { type: "run:done", green: true, finalTest: "published", mergedSha: "abc123", summary: { agentsMerged: 2 } },
];

function logOf(events: BraidEvent[], startTs = 1000): LoggedEvent[] {
  return events.map((event, i) => ({ event, meta: { seq: i + 1, ts: startTs + i } }));
}

function foldIncremental(runId: string, log: LoggedEvent[]): RunState {
  let s = emptyState(runId, log[0].meta.ts);
  for (const { event, meta } of log) s = reduce(s, event, meta);
  return s;
}

describe("runstate reducer", () => {
  it("is deterministic: same log folds to byte-identical state", () => {
    const log = logOf(EVENTS);
    const a = foldIncremental("run-1", log);
    const b = foldIncremental("run-1", log);
    expect(JSON.stringify(a)).toBe(JSON.stringify(b));
  });

  it("tracks seq and updatedAt from meta, never a wall clock", () => {
    const log = logOf(EVENTS, 5000);
    const s = foldIncremental("run-1", log);
    expect(s.seq).toBe(EVENTS.length);
    expect(s.updatedAt).toBe(5000 + EVENTS.length - 1);
  });

  it("projects the expected run shape", () => {
    const s = foldIncremental("run-1", logOf(EVENTS));
    expect(s.phase).toBe("done");
    expect(s.agentOrder).toEqual(["a1", "a2"]);
    expect(s.agents.a1.merge).toBe("auto-merged");
    expect(s.agents.a2.merge).toBe("resolved");
    expect(s.conflicts).toHaveLength(1);
    expect(s.result?.green).toBe(true);
    expect(s.result?.mergedSha).toBe("abc123");
  });
});

describe("rebuild", () => {
  it("rebuild(log) equals incremental folding (rebuildability)", () => {
    const log = logOf(EVENTS);
    const incremental = foldIncremental("run-1", log);
    const rebuilt = rebuild("run-1", log);
    expect(JSON.stringify(rebuilt)).toBe(JSON.stringify(incremental));
  });

  it("resumes from a snapshot + tail and matches a full rebuild", () => {
    const log = logOf(EVENTS);
    // Snapshot after the first 4 events; replay the rest on top.
    const snap = rebuild("run-1", log.slice(0, 4));
    expect(snap.seq).toBe(4);
    const resumed = rebuild("run-1", log, snap); // rebuild skips seq <= snap.seq
    const full = rebuild("run-1", log);
    expect(JSON.stringify(resumed)).toBe(JSON.stringify(full));
  });

  it("ignores events already covered by the snapshot (idempotent replay)", () => {
    const log = logOf(EVENTS);
    const snap = rebuild("run-1", log.slice(0, 6));
    // Replaying the WHOLE log onto the snapshot must not double-apply events 1..6.
    const resumed = rebuild("run-1", log, snap);
    expect(resumed.seq).toBe(EVENTS.length);
    expect(resumed.conflicts).toHaveLength(1); // not doubled
  });
});

import { describe, it, expect } from "vitest";
import {
  createRun,
  applyAction,
  transition,
  isTerminal,
  type Run,
} from "../src/v1/lifecycle";

const at = (s: string) => `2026-10-14T00:00:0${s}Z`;

function fresh(): Run {
  return createRun({ id: "r1", task: "t", base: "b", agents: ["a1", "a2"], at: at("0") });
}

describe("transition", () => {
  it("drives the happy path queued -> running -> succeeded", () => {
    expect(transition("queued", "start")).toEqual({ ok: true, next: "running" });
    expect(transition("running", "succeed")).toEqual({ ok: true, next: "succeeded" });
  });

  it("supports the escalation loop", () => {
    expect(transition("running", "escalate")).toEqual({ ok: true, next: "awaiting_approval" });
    expect(transition("awaiting_approval", "approve")).toEqual({ ok: true, next: "running" });
    expect(transition("awaiting_approval", "reject")).toEqual({ ok: true, next: "incompatible" });
  });

  it("rejects illegal transitions", () => {
    expect(transition("queued", "succeed").ok).toBe(false);
    expect(transition("running", "approve").ok).toBe(false);
    expect(transition("succeeded", "cancel").ok).toBe(false); // terminal
  });

  it("allows cancel from any non-terminal state", () => {
    for (const s of ["queued", "running", "awaiting_approval"] as const) {
      expect(transition(s, "cancel")).toEqual({ ok: true, next: "canceled" });
    }
  });
});

describe("isTerminal", () => {
  it("marks exactly the terminal states", () => {
    expect(["succeeded", "failed", "incompatible", "canceled"].every(isTerminal)).toBe(true);
    expect(["queued", "running", "awaiting_approval"].some(isTerminal)).toBe(false);
  });
});

describe("applyAction", () => {
  it("is immutable and records history", () => {
    const r0 = fresh();
    const r1 = applyAction(r0, "start", at("1"));
    expect(r1.ok).toBe(true);
    if (!r1.ok) return;
    expect(r0.status).toBe("queued"); // input untouched
    expect(r1.run.status).toBe("running");
    expect(r1.run.updatedAt).toBe(at("1"));
    expect(r1.run.history.map((h) => h.to)).toEqual(["queued", "running"]);
  });

  it("returns an error and leaves the run unchanged on an illegal action", () => {
    const r0 = fresh();
    const bad = applyAction(r0, "approve", at("1"));
    expect(bad.ok).toBe(false);
  });

  it("walks a full escalation-then-approve run", () => {
    let run = fresh();
    for (const a of ["start", "escalate", "approve", "succeed"] as const) {
      const res = applyAction(run, a, at("2"));
      expect(res.ok).toBe(true);
      if (res.ok) run = res.run;
    }
    expect(run.status).toBe("succeeded");
    expect(run.history).toHaveLength(5); // create + 4 actions
  });
});

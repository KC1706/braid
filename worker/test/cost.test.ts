import { describe, it, expect } from "vitest";
import { estimateCost, factsFromRun, DEFAULT_PRICES, type RunFacts } from "../src/v1/cost";

const FACTS: RunFacts = {
  computeVcpuSec: 30, // ~43s wall, mostly `go test`
  computeGiBSec: 180,
  ioVcpuSec: 2,
  ioGiBSec: 8,
  aiCalls: 2,
  requests: 40,
  r2Writes: 5,
  publishedBytes: 2 * 1024 * 1024, // 2 MiB merged tree
};

describe("estimateCost", () => {
  it("itemizes and totals a run cost, sorted most-expensive first", () => {
    const e = estimateCost(FACTS, 0.1);
    expect(e.lines.length).toBe(6);
    // sorted descending by usd
    for (let i = 1; i < e.lines.length; i++) {
      expect(e.lines[i - 1].usd).toBeGreaterThanOrEqual(e.lines[i].usd);
    }
    // total equals the sum of lines
    const sum = e.lines.reduce((s, l) => s + l.usd, 0);
    expect(e.costUsd).toBeCloseTo(sum, 6);
  });

  it("the economics gate: published price yields a positive margin", () => {
    // A representative run costs well under a dime; price it at $0.10.
    const e = estimateCost(FACTS, 0.1);
    expect(e.costUsd).toBeGreaterThan(0);
    expect(e.costUsd).toBeLessThan(0.1);
    expect(e.marginUsd).toBeGreaterThan(0); // <-- positive $/run margin
    expect(e.marginPct).toBeGreaterThan(0);
  });

  it("margin goes negative if the price is below cost (honest math)", () => {
    const e = estimateCost(FACTS, 0.0001);
    expect(e.marginUsd).toBeLessThan(0);
  });

  it("AI conflict merges dominate a conflict-heavy run", () => {
    const heavy = estimateCost({ ...FACTS, aiCalls: 20 }, 1);
    expect(heavy.lines[0].item).toBe("AI conflict merges");
  });

  it("uses the default price sheet when none is given", () => {
    const e = estimateCost(FACTS, 0.1);
    const aiLine = e.lines.find((l) => l.item === "AI conflict merges")!;
    expect(aiLine.unitUsd).toBe(DEFAULT_PRICES.aiCall);
  });
});

describe("factsFromRun", () => {
  it("derives conservative facts from a run's wall time + swarm size", () => {
    const f = factsFromRun({ wallMs: 60_000, agents: 3, aiCalls: 1, requests: 30, r2Writes: 4, publishedBytes: 1024 });
    // 60s wall: compute ~70% at 1 vCPU, io ~15% at 1/4 vCPU
    expect(f.computeVcpuSec).toBeCloseTo(42, 3);
    expect(f.computeGiBSec).toBeCloseTo(252, 3); // 42 * 6
    expect(f.ioVcpuSec).toBeCloseTo(2.25, 3); // 9 * 0.25
    expect(f.aiCalls).toBe(1);
  });

  it("feeds straight into estimateCost for a positive end-to-end number", () => {
    const f = factsFromRun({ wallMs: 45_000, agents: 3, aiCalls: 2, requests: 40, r2Writes: 5, publishedBytes: 2 * 1024 * 1024 });
    const e = estimateCost(f, 0.1);
    expect(e.marginUsd).toBeGreaterThan(0);
  });
});

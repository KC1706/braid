import { describe, it, expect } from "vitest";
import {
  initialState,
  beginPublish,
  commitPublish,
  abortPublish,
  type CoordinatorState,
} from "../src/v1/coordinator";

const T0 = "2026-10-14T00:00:00.000Z";
const later = (base: string, secs: number) => new Date(Date.parse(base) + secs * 1000).toISOString();

describe("Base-Coordinator", () => {
  it("grants the first run and adopts its base as the tip (TOFU)", () => {
    const { state, result } = beginPublish(initialState(), T0, "run-A", "sha-base", 60);
    expect(result.status).toBe("granted");
    expect(state.currentTip).toBe("sha-base");
    expect(state.lease?.holder).toBe("run-A");
  });

  it("serializes: a second run is busy while another holds a live lease", () => {
    const g = beginPublish(initialState(), T0, "run-A", "sha-base", 60);
    const { result } = beginPublish(g.state, T0, "run-B", "sha-base", 60);
    expect(result).toEqual({ status: "busy", holder: "run-A" });
  });

  it("CAS: a run whose base is stale gets base_moved, not the lease", () => {
    // run-A publishes, advancing the tip to sha-1.
    let s: CoordinatorState = beginPublish(initialState(), T0, "run-A", "sha-base", 60).state;
    s = commitPublish(s, T0, "run-A", "sha-1").state;
    // run-B merged from the old base sha-base -> stale.
    const { state, result } = beginPublish(s, T0, "run-B", "sha-base", 60);
    expect(result).toEqual({ status: "base_moved", currentTip: "sha-1" });
    expect(state.lease).toBeNull(); // not granted
  });

  it("commit advances the tip and releases the lease", () => {
    const g = beginPublish(initialState(), T0, "run-A", "sha-base", 60);
    const { state, result } = commitPublish(g.state, T0, "run-A", "sha-1");
    expect(result).toEqual({ status: "ok", currentTip: "sha-1" });
    expect(state.currentTip).toBe("sha-1");
    expect(state.lease).toBeNull();
  });

  it("a non-holder cannot commit (a timed-out run can't overwrite a newer one)", () => {
    const g = beginPublish(initialState(), T0, "run-A", "sha-base", 60);
    const { state, result } = commitPublish(g.state, T0, "run-B", "sha-evil");
    expect(result).toEqual({ status: "not_holder" });
    expect(state.currentTip).toBe("sha-base"); // unchanged
  });

  it("re-granting to the current holder is idempotent (safe step retry)", () => {
    const g1 = beginPublish(initialState(), T0, "run-A", "sha-base", 60);
    const g2 = beginPublish(g1.state, later(T0, 5), "run-A", "sha-base", 60);
    expect(g2.result.status).toBe("granted");
    expect(g2.state.lease?.holder).toBe("run-A");
  });

  it("an expired lease frees the base for another run", () => {
    const g = beginPublish(initialState(), T0, "run-A", "sha-base", 60);
    // 61s later run-A's lease has expired; run-B may acquire with the correct tip.
    const { result } = beginPublish(g.state, later(T0, 61), "run-B", "sha-base", 60);
    expect(result.status).toBe("granted");
  });

  it("a timed-out holder's commit is rejected (lease expired)", () => {
    const g = beginPublish(initialState(), T0, "run-A", "sha-base", 60);
    const { result } = commitPublish(g.state, later(T0, 61), "run-A", "sha-1");
    expect(result).toEqual({ status: "not_holder" });
  });

  it("abort releases a held lease without advancing the tip", () => {
    const g = beginPublish(initialState(), T0, "run-A", "sha-base", 60);
    const s = abortPublish(g.state, T0, "run-A");
    expect(s.lease).toBeNull();
    expect(s.currentTip).toBe("sha-base");
    // another run can now acquire
    expect(beginPublish(s, T0, "run-B", "sha-base", 60).result.status).toBe("granted");
  });

  it("two racing runs on one base: exactly one publishes, the other rebases", () => {
    // Both merged from sha-base concurrently.
    let s = initialState();
    const a = beginPublish(s, T0, "run-A", "sha-base", 60);
    s = a.state;
    expect(a.result.status).toBe("granted");
    // run-B tries while A holds -> busy (must wait).
    expect(beginPublish(s, T0, "run-B", "sha-base", 60).result.status).toBe("busy");
    // A commits -> tip advances.
    s = commitPublish(s, T0, "run-A", "sha-1").state;
    // B retries -> base_moved (its sha-base is stale) -> must rebase onto sha-1.
    expect(beginPublish(s, T0, "run-B", "sha-base", 60).result).toEqual({ status: "base_moved", currentTip: "sha-1" });
  });
});

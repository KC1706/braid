import { describe, it, expect } from "vitest";
import { runIntegration, isSelfPush, type IntegrationDeps, type IntegrationJob, type RunEvent } from "../src/v1/integration";
import {
  BaseMovedError,
  type ComputeZoneClient,
  type IOZoneClient,
  type IntegrateRequest,
  type IntegrateResponse,
  type PublishRequest,
  type PublishResponse,
  type SnapshotRequest,
  type SnapshotResponse,
} from "../src/v1/ztypes";

const job: IntegrationJob = {
  runId: "run-1",
  baseKey: "base-repo",
  task: "t",
  eventSecret: "sek",
  base: { remote: "https://base", branch: "main", token: { plaintext: "base-tok" } },
  agents: [
    { id: "a1", remote: "https://a1", branch: "main", intent: "i1", token: { plaintext: "a1-tok" } },
    { id: "a2", remote: "https://a2", branch: "main", intent: "i2", token: { plaintext: "a2-tok" } },
  ],
};

class FakeIO implements IOZoneClient {
  snapshots: SnapshotRequest[] = [];
  publishes: PublishRequest[] = [];
  publishImpl: (r: PublishRequest) => Promise<PublishResponse> = async () => ({ pushedSha: "merged-sha" });
  async snapshot(req: SnapshotRequest): Promise<SnapshotResponse> {
    this.snapshots.push(req);
    return { bundle: "BUNDLE", baselineSha: "base-sha", branches: { a1: "s1", a2: "s2" } };
  }
  async publish(req: PublishRequest): Promise<PublishResponse> {
    this.publishes.push(req);
    return this.publishImpl(req);
  }
}

class FakeCompute implements ComputeZoneClient {
  requests: IntegrateRequest[] = [];
  resp: IntegrateResponse = {
    verdict: "green", green: true, baseSha: "base-sha", mergedSha: "merged-sha",
    ref: "braid/integration", mergedBundle: "MERGED", summary: { agentsMerged: 2 },
  };
  async integrate(req: IntegrateRequest): Promise<IntegrateResponse> {
    this.requests.push(req);
    return this.resp;
  }
}

function mkDeps(io: FakeIO, compute: FakeCompute) {
  const events: RunEvent[] = [];
  const pushed: string[] = [];
  const deps: IntegrationDeps = {
    io,
    compute,
    emit: async (e) => { events.push(e); },
    recordPush: async (sha) => { pushed.push(sha); },
    now: () => "2026-10-14T00:00:00Z",
  };
  return { deps, events, pushed };
}

describe("runIntegration", () => {
  it("green path: snapshot -> integrate -> publish, records the pushed sha", async () => {
    const io = new FakeIO(), compute = new FakeCompute();
    const { deps, events, pushed } = mkDeps(io, compute);
    const out = await runIntegration(deps, job);

    expect(out).toEqual({ status: "succeeded", verdict: "green", mergedSha: "merged-sha", pushedSha: "merged-sha" });
    expect(io.snapshots).toHaveLength(1);
    expect(io.publishes).toHaveLength(1);
    expect(pushed).toEqual(["merged-sha"]);
    expect(events.some((e) => e.type === "run:done" && e.green === true)).toBe(true);
  });

  it("hands the compute zone NO token (token isolation, C13/C15)", async () => {
    const io = new FakeIO(), compute = new FakeCompute();
    const { deps } = mkDeps(io, compute);
    await runIntegration(deps, job);

    const req = compute.requests[0];
    const serialized = JSON.stringify(req);
    expect(serialized).not.toContain("tok"); // no "*-tok" plaintext anywhere
    expect(serialized).not.toContain("remote");
    // agents carry only id + intent
    expect(req.agents).toEqual([{ id: "a1", intent: "i1" }, { id: "a2", intent: "i2" }]);
    // the io zone, by contrast, DID receive the tokens
    expect(JSON.stringify(io.snapshots[0])).toContain("a1-tok");
  });

  it("red (failed) path: does NOT publish", async () => {
    const io = new FakeIO(), compute = new FakeCompute();
    compute.resp = { ...compute.resp, green: false, verdict: "red", mergedBundle: undefined };
    const { deps, events } = mkDeps(io, compute);
    const out = await runIntegration(deps, job);

    expect(out.status).toBe("failed");
    expect(io.publishes).toHaveLength(0);
    expect(events.some((e) => e.type === "run:done" && e.green === false)).toBe(true);
  });

  it("incompatible verdict maps to status incompatible", async () => {
    const io = new FakeIO(), compute = new FakeCompute();
    compute.resp = { ...compute.resp, green: false, verdict: "incompatible", mergedBundle: undefined };
    const { deps } = mkDeps(io, compute);
    const out = await runIntegration(deps, job);
    expect(out.status).toBe("incompatible");
    expect(io.publishes).toHaveLength(0);
  });

  it("CAS conflict: publish throws BaseMovedError -> status base_moved, no recordPush", async () => {
    const io = new FakeIO(), compute = new FakeCompute();
    io.publishImpl = async () => { throw new BaseMovedError(); };
    const { deps, pushed } = mkDeps(io, compute);
    const out = await runIntegration(deps, job);
    expect(out.status).toBe("base_moved");
    expect(pushed).toHaveLength(0);
  });
});

describe("isSelfPush (C9)", () => {
  it("matches a previously published sha and ignores others", () => {
    const published = new Set(["aaa", "bbb"]);
    expect(isSelfPush(published, "aaa")).toBe(true);
    expect(isSelfPush(published, "ccc")).toBe(false);
    expect(isSelfPush(published, "")).toBe(false);
    expect(isSelfPush([], "aaa")).toBe(false);
  });
});

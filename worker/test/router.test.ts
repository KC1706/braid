import { describe, it, expect } from "vitest";
import { handleV1, type Deps, type Provision, type RunsStore, type RunSummary } from "../src/v1/router";
import { MapTokenStore, type Principal } from "../src/v1/auth";
import { MapIdempotencyStore } from "../src/v1/idempotency";
import type { Run } from "../src/v1/lifecycle";

const operator: Principal = { id: "op", roles: ["operator"] };
const viewer: Principal = { id: "v", roles: ["viewer"] };

class MemRuns implements RunsStore {
  map = new Map<string, { run: Run; secret: string }>();
  async create(run: Run, secret: string) {
    this.map.set(run.id, { run, secret });
  }
  async get(id: string) {
    return this.map.get(id) ?? null;
  }
  async update(run: Run) {
    const e = this.map.get(run.id);
    if (e) e.run = run;
  }
  async list(limit = 50): Promise<RunSummary[]> {
    return [...this.map.values()]
      .map(({ run }) => ({
        id: run.id,
        status: run.status,
        task: run.task,
        base: run.base,
        agents: run.agents.length,
        createdAt: run.createdAt,
        updatedAt: run.updatedAt,
        verdict: run.verdict,
        mergedSha: run.mergedSha,
      }))
      .sort((a, b) => (a.createdAt < b.createdAt ? 1 : -1))
      .slice(0, limit);
  }
}

function deps(overrides: Partial<Deps> = {}): Deps & { runs: MemRuns; enqueued: Run[] } {
  const runs = new MemRuns();
  const enqueued: Run[] = [];
  let idSeq = 0;
  const base: Deps = {
    tokens: new MapTokenStore(new Map([["op", operator], ["view", viewer]])),
    idem: new MapIdempotencyStore(),
    runs,
    forker: {
      async provision(base: string, agents: string[]): Promise<Provision> {
        return {
          base: { remote: `https://r/${base}`, branch: "main", token: `base-tok` },
          forks: agents.map((a) => ({ agent: a, name: `${base}-${a}`, remote: `https://r/${a}`, token: `tok-${a}` })),
        };
      },
    },
    enqueue: async (run) => {
      enqueued.push(run);
    },
    now: () => "2026-10-14T00:00:00Z",
    newId: () => `run-${++idSeq}`,
    newSecret: () => "secret-xyz",
    ...overrides,
  };
  return Object.assign(base, { runs, enqueued });
}

const post = (path: string, token: string | undefined, body?: unknown, headers: Record<string, string> = {}) =>
  new Request(`https://x${path}`, {
    method: "POST",
    headers: { ...(token ? { authorization: `Bearer ${token}` } : {}), ...headers },
    body: body === undefined ? undefined : JSON.stringify(body),
  });

const get = (path: string, token?: string) =>
  new Request(`https://x${path}`, { headers: token ? { authorization: `Bearer ${token}` } : {} });

describe("handleV1 routing", () => {
  it("returns null for non-/v1 paths", async () => {
    expect(await handleV1(new Request("https://x/api/health"), deps())).toBeNull();
  });
});

describe("POST /v1/runs", () => {
  it("creates, forks, enqueues, and returns 201 with a secret", async () => {
    const d = deps();
    const res = await handleV1(post("/v1/runs", "op", { base: "b", agents: ["a1", "a2"], task: "t" }), d);
    expect(res!.status).toBe(201);
    const body = await res!.json() as any;
    expect(body).toMatchObject({ id: "run-1", status: "queued", base: "b", eventSecret: "secret-xyz" });
    expect(body.forks).toHaveLength(2);
    expect(d.enqueued).toHaveLength(1);
    expect(d.runs.map.get("run-1")?.secret).toBe("secret-xyz");
  });

  it("401 without a token, 403 for a viewer", async () => {
    expect((await handleV1(post("/v1/runs", undefined, { base: "b" }), deps()))!.status).toBe(401);
    expect((await handleV1(post("/v1/runs", "view", { base: "b" }), deps()))!.status).toBe(403);
  });

  it("400 when base is missing", async () => {
    const res = await handleV1(post("/v1/runs", "op", { agents: ["a"] }), deps());
    expect(res!.status).toBe(400);
  });

  it("replays the same result for a repeated Idempotency-Key, and does not double-fork", async () => {
    const d = deps();
    const first = await handleV1(post("/v1/runs", "op", { base: "b" }, { "idempotency-key": "k1" }), d);
    const firstBody = await first!.json() as any;
    const retry = await handleV1(post("/v1/runs", "op", { base: "b" }, { "idempotency-key": "k1" }), d);
    const retryBody = await retry!.json() as any;
    expect(retry!.status).toBe(201);
    expect(retryBody).toEqual(firstBody); // same id, verbatim
    expect(d.enqueued).toHaveLength(1); // enqueued once, not twice
    expect(d.runs.map.size).toBe(1);
  });

  it("409 when an Idempotency-Key is reused with a different body", async () => {
    const d = deps();
    await handleV1(post("/v1/runs", "op", { base: "b1" }, { "idempotency-key": "k2" }), d);
    const conflict = await handleV1(post("/v1/runs", "op", { base: "b2" }, { "idempotency-key": "k2" }), d);
    expect(conflict!.status).toBe(409);
  });
});

describe("GET /v1/runs/:id", () => {
  it("reads a run (no secret in the body) and 404s an unknown id", async () => {
    const d = deps();
    await handleV1(post("/v1/runs", "op", { base: "b" }), d);
    const res = await handleV1(get("/v1/runs/run-1", "view"), d);
    expect(res!.status).toBe(200);
    const body = await res!.json() as any;
    expect(body.id).toBe("run-1");
    expect(body.eventSecret).toBeUndefined();
    expect((await handleV1(get("/v1/runs/nope", "view"), d))!.status).toBe(404);
  });
});

describe("GET /v1/runs (console run list)", () => {
  it("lists run summaries newest-first; requires auth; no secret leak", async () => {
    const d = deps();
    await handleV1(post("/v1/runs", "op", { base: "b1", task: "first" }), d);
    await handleV1(post("/v1/runs", "op", { base: "b2", task: "second" }), d);
    expect((await handleV1(get("/v1/runs"), d))!.status).toBe(401); // no token
    const res = await handleV1(get("/v1/runs", "view"), d);
    expect(res!.status).toBe(200);
    const body = (await res!.json()) as any;
    expect(body.runs).toHaveLength(2);
    expect(body.runs[0].id).toBe("run-2"); // newest first
    expect(body.runs[0]).not.toHaveProperty("eventSecret");
    expect(body.runs[0].agents).toBe(3); // default agent count, as a number
  });

  it("honors ?limit", async () => {
    const d = deps();
    await handleV1(post("/v1/runs", "op", { base: "b1" }), d);
    await handleV1(post("/v1/runs", "op", { base: "b2" }), d);
    const res = await handleV1(get("/v1/runs?limit=1", "view"), d);
    expect(((await res!.json()) as any).runs).toHaveLength(1);
  });
});

describe("GET /v1/runs/:id/cost (economics)", () => {
  it("publishes a per-run cost + positive margin", async () => {
    const d = deps({
      projection: {
        state: async () => ({ seq: 6, conflicts: [{}, {}], result: { green: true } }),
        events: async () => ({ events: [] }),
        rebuild: async () => ({}),
      },
      runPriceUsd: 0.1,
    });
    await handleV1(post("/v1/runs", "op", { base: "b" }), d);
    const res = await handleV1(get("/v1/runs/run-1/cost", "view"), d);
    expect(res!.status).toBe(200);
    const body = (await res!.json()) as any;
    expect(body.runId).toBe("run-1");
    expect(body.priceUsd).toBe(0.1);
    expect(body.costUsd).toBeGreaterThan(0);
    expect(body.marginUsd).toBeGreaterThan(0);
    expect(Array.isArray(body.lines)).toBe(true);
  });

  it("404s an unknown run", async () => {
    const d = deps();
    expect((await handleV1(get("/v1/runs/nope/cost", "view"), d))!.status).toBe(404);
  });
});

describe("GET /v1/runs/:id/live|events|rebuild (authenticated projection)", () => {
  it("proxies the DO projection behind runs:read; 501 without a projection dep", async () => {
    const withProj = deps({
      projection: {
        state: async (id) => ({ runId: id, phase: "done", seq: 3 }),
        events: async (id, since) => ({ runId: id, since, events: [{ meta: { seq: 1 } }] }),
        rebuild: async (id) => ({ runId: id, rebuilt: true }),
      },
    });
    await handleV1(post("/v1/runs", "op", { base: "b" }), withProj);
    const live = await handleV1(get("/v1/runs/run-1/live", "view"), withProj);
    expect(((await live!.json()) as any).phase).toBe("done");
    const evs = await handleV1(get("/v1/runs/run-1/events?since=0", "view"), withProj);
    expect(((await evs!.json()) as any).events).toHaveLength(1);
    const rb = await handleV1(get("/v1/runs/run-1/rebuild", "view"), withProj);
    expect(((await rb!.json()) as any).rebuilt).toBe(true);

    const noProj = deps();
    await handleV1(post("/v1/runs", "op", { base: "b" }), noProj);
    expect((await handleV1(get("/v1/runs/run-1/live", "view"), noProj))!.status).toBe(501);
  });
});

describe("lifecycle actions", () => {
  it("cancel transitions queued -> canceled; a second cancel is 409", async () => {
    const d = deps();
    await handleV1(post("/v1/runs", "op", { base: "b" }), d);
    const c1 = await handleV1(post("/v1/runs/run-1/cancel", "op"), d);
    expect(c1!.status).toBe(200);
    expect(((await c1!.json()) as any).status).toBe("canceled");
    const c2 = await handleV1(post("/v1/runs/run-1/cancel", "op"), d);
    expect(c2!.status).toBe(409); // terminal
  });

  it("approve is 409 from queued (not awaiting_approval) and needs runs:approve", async () => {
    const d = deps();
    await handleV1(post("/v1/runs", "op", { base: "b" }), d);
    expect((await handleV1(post("/v1/runs/run-1/approve", "view"), d))!.status).toBe(403);
    expect((await handleV1(post("/v1/runs/run-1/approve", "op"), d))!.status).toBe(409);
  });
});

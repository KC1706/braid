/**
 * BaseCoordinator Durable Object — the per-base singleton that persists the
 * coordinator state machine (coordinator.ts) and serializes publishes to one
 * base (GO_PRODUCTION_PLAN.md §3 Phase 4a, C8). One instance per base key
 * (idFromName(baseKey)), so all runs targeting a base contend on the same lease +
 * tip. The DO is the serialization point; the state transitions are the pure
 * functions, so the DO is a thin persistence shell.
 */

import {
  initialState,
  beginPublish,
  commitPublish,
  abortPublish,
  type BeginResult,
  type CommitResult,
  type CoordinatorState,
} from "./coordinator";

interface DOState {
  storage: {
    get<T>(key: string): Promise<T | undefined>;
    put<T>(key: string, value: T): Promise<void>;
  };
  blockConcurrencyWhile(fn: () => Promise<void>): void;
}

const KEY = "coordinator";
const json = (data: unknown, status = 200) =>
  new Response(JSON.stringify(data), { status, headers: { "content-type": "application/json" } });

export class BaseCoordinator {
  private state!: CoordinatorState;
  private loaded: Promise<void>;

  constructor(private ctx: DOState) {
    this.loaded = new Promise((resolve) => {
      this.ctx.blockConcurrencyWhile(async () => {
        this.state = (await this.ctx.storage.get<CoordinatorState>(KEY)) ?? initialState();
        resolve();
      });
    });
  }

  async fetch(req: Request): Promise<Response> {
    await this.loaded;
    const url = new URL(req.url);
    const now = new Date().toISOString();

    if (url.pathname.endsWith("/begin") && req.method === "POST") {
      const { runId, expectedBaseSha, ttlSeconds } = (await req.json()) as {
        runId: string;
        expectedBaseSha: string;
        ttlSeconds?: number;
      };
      const { state, result } = beginPublish(this.state, now, runId, expectedBaseSha, ttlSeconds ?? 300);
      if (result.status === "granted") {
        this.state = state;
        await this.ctx.storage.put(KEY, this.state);
      }
      return json(result satisfies BeginResult);
    }

    if (url.pathname.endsWith("/commit") && req.method === "POST") {
      const { runId, newTip } = (await req.json()) as { runId: string; newTip: string };
      const { state, result } = commitPublish(this.state, now, runId, newTip);
      if (result.status === "ok") {
        this.state = state;
        await this.ctx.storage.put(KEY, this.state);
      }
      return json(result satisfies CommitResult);
    }

    if (url.pathname.endsWith("/abort") && req.method === "POST") {
      const { runId } = (await req.json()) as { runId: string };
      this.state = abortPublish(this.state, now, runId);
      await this.ctx.storage.put(KEY, this.state);
      return json({ ok: true });
    }

    if (url.pathname.endsWith("/state")) {
      return json(this.state);
    }
    return json({ error: "not found" }, 404);
  }
}

/** The DO namespace shape the client needs. */
export interface CoordinatorNamespace {
  idFromName(name: string): unknown;
  get(id: unknown): { fetch(req: Request): Promise<Response> };
}

const DO = "https://do";

/**
 * BaseCoordinatorClient routes begin/commit/abort to the base's coordinator DO.
 * The Workflow wraps its publish step with begin -> io.publish -> commit (abort on
 * failure), so a publish only runs while this run holds the base's lease and its
 * parent SHA is still current.
 */
export class BaseCoordinatorClient {
  constructor(private readonly ns: CoordinatorNamespace) {}
  private stub(baseKey: string) {
    return this.ns.get(this.ns.idFromName(baseKey));
  }
  async begin(baseKey: string, runId: string, expectedBaseSha: string, ttlSeconds: number): Promise<BeginResult> {
    const res = await this.stub(baseKey).fetch(
      new Request(`${DO}/begin`, { method: "POST", body: JSON.stringify({ runId, expectedBaseSha, ttlSeconds }) }),
    );
    return (await res.json()) as BeginResult;
  }
  async commit(baseKey: string, runId: string, newTip: string): Promise<CommitResult> {
    const res = await this.stub(baseKey).fetch(
      new Request(`${DO}/commit`, { method: "POST", body: JSON.stringify({ runId, newTip }) }),
    );
    return (await res.json()) as CommitResult;
  }
  async abort(baseKey: string, runId: string): Promise<void> {
    await this.stub(baseKey).fetch(new Request(`${DO}/abort`, { method: "POST", body: JSON.stringify({ runId }) }));
  }
}

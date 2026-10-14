/**
 * Runtime-backed implementations of the /v1 router's dependency interfaces,
 * wiring the pure control plane to the Orchestrator Durable Object and the
 * Artifacts binding. These need the Workers runtime, so they are exercised by a
 * deployed smoke test rather than the node unit suite (which uses the in-memory
 * fakes). Keeping them here keeps index.ts thin and the interfaces honest.
 */

import type { Fork, Forker, Provision, RunsStore, RunSummary } from "./router";
import type { IdempotencyRecord, IdempotencyStore } from "./idempotency";
import type { Run } from "./lifecycle";

/** The subset of an Artifacts repo handle the forker uses. */
export interface ArtifactsHandle {
  fork(name: string, opts?: { defaultBranchOnly?: boolean }): Promise<{ remote: string }>;
  createToken(scope: "read" | "write", ttl: number): Promise<{ plaintext: string; expiresAt: string }>;
  info(): Promise<{ remote: string; defaultBranch?: string }>;
}
/** The subset of the Artifacts binding the forker uses. */
export interface ArtifactsBinding {
  get(name: string): Promise<ArtifactsHandle>;
}

/** The DO namespace shape the stores use (a subset of DurableObjectNamespace). */
export interface DONamespace {
  idFromName(name: string): unknown;
  get(id: unknown): { fetch(req: Request): Promise<Response> };
}

/**
 * ArtifactsForker provisions, off a seeded base: one fork + short-lived WRITE
 * token per agent (the agent pushes its work to its own fork), plus the base's
 * own remote + a short-lived WRITE token for publishing the merged result. All
 * tokens are 1-hour scoped and only ever reach the trusted io zone.
 */
export class ArtifactsForker implements Forker {
  constructor(private readonly artifacts: ArtifactsBinding) {}
  async provision(base: string, agents: string[]): Promise<Provision> {
    const baseHandle = await this.artifacts.get(base);
    const info = await baseHandle.info();
    const baseTok = await baseHandle.createToken("write", 3600);

    const forks: Fork[] = [];
    for (const agent of agents) {
      const name = `${base}-${agent}`;
      const forked = await baseHandle.fork(name, { defaultBranchOnly: true });
      const tok = await (await this.artifacts.get(name)).createToken("write", 3600);
      forks.push({ agent, name, remote: forked.remote, token: tok.plaintext, expiresAt: tok.expiresAt });
    }
    return {
      base: { remote: info.remote, branch: info.defaultBranch ?? "main", token: baseTok.plaintext, expiresAt: baseTok.expiresAt },
      forks,
    };
  }
}

/** A base URL for internal DO fetches; the host is ignored by the DO. */
const DO = "https://do";

/**
 * DORunsStore keeps each run's lifecycle record and per-run HMAC secret inside
 * that run's own Orchestrator DO instance (idFromName(run.id)), so event
 * verification is local to the DO that ingests the events.
 */
export class DORunsStore implements RunsStore {
  constructor(private readonly ns: DONamespace, private readonly controlName = "__control__") {}
  private stub(id: string) {
    return this.ns.get(this.ns.idFromName(id));
  }
  private control() {
    return this.ns.get(this.ns.idFromName(this.controlName));
  }
  // NB: the per-run DO upserts its own summary into the control index on every
  // run write (init + record PUT), so BOTH the control plane AND the Workflow's
  // direct record writes keep the console's run list current. list() reads it.
  async create(run: Run, secret: string): Promise<void> {
    const res = await this.stub(run.id).fetch(
      new Request(`${DO}/runs/${run.id}/init`, { method: "POST", body: JSON.stringify({ run, secret }) }),
    );
    if (!res.ok) throw new Error(`DORunsStore.create: ${res.status}`);
  }
  async get(id: string): Promise<{ run: Run; secret: string } | null> {
    const res = await this.stub(id).fetch(new Request(`${DO}/runs/${id}/record`));
    if (res.status === 404) return null;
    if (!res.ok) throw new Error(`DORunsStore.get: ${res.status}`);
    return (await res.json()) as { run: Run; secret: string };
  }
  async update(run: Run): Promise<void> {
    const res = await this.stub(run.id).fetch(
      new Request(`${DO}/runs/${run.id}/record`, { method: "PUT", body: JSON.stringify(run) }),
    );
    if (!res.ok) throw new Error(`DORunsStore.update: ${res.status}`);
  }
  async list(limit = 50): Promise<RunSummary[]> {
    const res = await this.control().fetch(new Request(`${DO}/index?limit=${limit}`));
    if (!res.ok) throw new Error(`DORunsStore.list: ${res.status}`);
    return ((await res.json()) as { runs: RunSummary[] }).runs;
  }
}

/**
 * DOIdempotencyStore holds idempotency records in one well-known control DO
 * instance, so a create can be deduped before any run id exists.
 */
export class DOIdempotencyStore implements IdempotencyStore {
  constructor(private readonly ns: DONamespace, private readonly controlName = "__control__") {}
  private stub() {
    return this.ns.get(this.ns.idFromName(this.controlName));
  }
  async get(key: string): Promise<IdempotencyRecord | null> {
    const res = await this.stub().fetch(new Request(`${DO}/idem/${encodeURIComponent(key)}`));
    if (res.status === 404) return null;
    if (!res.ok) throw new Error(`DOIdempotencyStore.get: ${res.status}`);
    return (await res.json()) as IdempotencyRecord;
  }
  async put(key: string, record: IdempotencyRecord): Promise<void> {
    const res = await this.stub().fetch(
      new Request(`${DO}/idem/${encodeURIComponent(key)}`, { method: "PUT", body: JSON.stringify(record) }),
    );
    if (!res.ok) throw new Error(`DOIdempotencyStore.put: ${res.status}`);
  }
}

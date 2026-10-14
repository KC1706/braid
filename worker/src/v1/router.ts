/**
 * The /v1 control-plane router (GO_PRODUCTION_PLAN.md §3 Phase 3b). It wires the
 * pure modules — auth/RBAC, idempotency, the run lifecycle — over injectable
 * dependencies (token store, idempotency store, runs store, a Forker, an enqueue
 * hook) so the whole request path is unit-testable in node with fakes; index.ts
 * supplies the DO/Artifacts/Queue-backed implementations.
 *
 *   POST /v1/runs                 runs:create — fork the base N times, mint a
 *                                 per-run HMAC secret, create a queued run,
 *                                 enqueue it. Idempotency-Key dedups retries.
 *   GET  /v1/runs/:id             runs:read
 *   POST /v1/runs/:id/cancel      runs:cancel
 *   POST /v1/runs/:id/approve     runs:approve (resume an escalated run)
 *   POST /v1/runs/:id/reject      runs:approve (reject an escalation)
 *
 * There is zero unauthenticated surface: every route authorizes first.
 */

import { authorize, type TokenStore } from "./auth";
import { begin, type IdempotencyStore } from "./idempotency";
import { applyAction, createRun, type Run, type RunAction } from "./lifecycle";
import { estimateCost, factsFromRun, type CostEstimate } from "./cost";

/** A forked agent repo, as the control plane provisions it. */
export interface Fork {
  agent: string;
  name: string;
  remote: string;
  token: string;
  expiresAt?: string;
}

/** BaseProvision is the base repo's own remote + a write token for publishing. */
export interface BaseProvision {
  remote: string;
  branch: string;
  token: string;
  expiresAt?: string;
}

/** Provision is the full set of repos + scoped tokens a run needs. */
export interface Provision {
  base: BaseProvision;
  forks: Fork[];
}

/**
 * Forker provisions, off a seeded base, one fork + read token per agent AND a
 * write token for the base itself (the publish target). All tokens are scoped and
 * short-lived; they are handed only to the trusted io zone.
 */
export interface Forker {
  provision(base: string, agents: string[]): Promise<Provision>;
}

/** A compact run row for the operator console's run list. */
export interface RunSummary {
  id: string;
  status: string;
  task: string;
  base: string;
  agents: number;
  createdAt: string;
  updatedAt: string;
  verdict?: string;
  mergedSha?: string;
}

/** RunsStore persists run records and their per-run HMAC secret. */
export interface RunsStore {
  create(run: Run, secret: string): Promise<void>;
  get(id: string): Promise<{ run: Run; secret: string } | null>;
  update(run: Run): Promise<void>;
  /** list returns run summaries, newest first, for the console (Phase 5a). */
  list(limit?: number): Promise<RunSummary[]>;
}

/**
 * Projection reads a run's live/rebuilt state + event log from the Orchestrator
 * DO (Phase 4c), so the console can poll an AUTHENTICATED view through /v1 rather
 * than the unauthenticated legacy /runs/:id surface. Optional: absent -> 501.
 */
export interface Projection {
  state(runId: string): Promise<RunStateView | null>;
  events(runId: string, since: number): Promise<unknown>;
  rebuild(runId: string): Promise<unknown>;
}

/** The subset of the DO projection the cost estimate + console consume. */
export interface RunStateView {
  seq?: number;
  log?: { ts: number; line: string }[];
  conflicts?: unknown[];
  result?: { green: boolean; summary?: Record<string, number> } | null;
  [k: string]: unknown;
}

export interface Deps {
  tokens: TokenStore;
  idem: IdempotencyStore;
  runs: RunsStore;
  forker: Forker;
  /** Enqueue the run for the Workflow consumer (Phase 3c). Gets the provisioned
   *  repos/tokens and the run's per-run HMAC secret (for signed event ingress). */
  enqueue: (run: Run, prov: Provision, secret: string) => Promise<void>;
  /** Optional reader for a run's signed provenance ledger (Phase 4b). */
  ledger?: { list(runId: string): Promise<unknown[]> };
  /** Optional reader for a run's live/rebuilt projection + log (Phase 4c/5a). */
  projection?: Projection;
  /** Published per-run price in USD for the economics view (Phase 5c). */
  runPriceUsd?: number;
  now: () => string;
  newId: () => string;
  newSecret: () => string;
}

const json = (data: unknown, status = 200) =>
  new Response(JSON.stringify(data, null, 2), {
    status,
    headers: { "content-type": "application/json" },
  });

interface CreateBody {
  base?: string;
  agents?: string[];
  task?: string;
}

/**
 * handleV1 routes a /v1 request, or returns null if the path is not under /v1 (so
 * the caller can fall through to legacy routes). It never throws: a bad body or
 * illegal transition becomes a 4xx.
 */
export async function handleV1(req: Request, deps: Deps): Promise<Response | null> {
  const url = new URL(req.url);
  if (!url.pathname.startsWith("/v1/")) return null;

  if (url.pathname === "/v1/runs" && req.method === "POST") {
    return createRunHandler(req, deps);
  }
  if (url.pathname === "/v1/runs" && req.method === "GET") {
    return listRunsHandler(req, deps, url);
  }

  const m = url.pathname.match(
    /^\/v1\/runs\/([A-Za-z0-9_-]+)(?:\/(cancel|approve|reject|ledger|live|events|rebuild|cost))?$/,
  );
  if (m) {
    const id = m[1];
    const verb = m[2];
    if (!verb && req.method === "GET") return getRunHandler(req, deps, id);
    if (verb === "ledger" && req.method === "GET") return ledgerHandler(req, deps, id);
    if (verb === "live" && req.method === "GET") return projectionHandler(req, deps, id, "state", url);
    if (verb === "events" && req.method === "GET") return projectionHandler(req, deps, id, "events", url);
    if (verb === "rebuild" && req.method === "GET") return projectionHandler(req, deps, id, "rebuild", url);
    if (verb === "cost" && req.method === "GET") return costHandler(req, deps, id);
    if (verb === "cancel" && req.method === "POST") return actionHandler(req, deps, id, "cancel", "runs:cancel");
    if (verb === "approve" && req.method === "POST") return actionHandler(req, deps, id, "approve", "runs:approve");
    if (verb === "reject" && req.method === "POST") return actionHandler(req, deps, id, "reject", "runs:approve");
    return json({ error: "method not allowed" }, 405);
  }

  return json({ error: "not found" }, 404);
}

async function createRunHandler(req: Request, deps: Deps): Promise<Response> {
  const auth = await authorize(req, deps.tokens, "runs:create");
  if (!auth.ok) return json({ error: auth.error }, auth.status);

  const raw = await req.text();
  const key = req.headers.get("idempotency-key");
  const idem = await begin(deps.idem, key, raw);
  if (idem.kind === "replay") return json(JSON.parse(idem.body), idem.status);
  if (idem.kind === "conflict") return json({ error: idem.error }, 409);

  let body: CreateBody;
  try {
    body = raw ? (JSON.parse(raw) as CreateBody) : {};
  } catch {
    return json({ error: "invalid JSON body" }, 400);
  }
  if (!body.base) return json({ error: "base is required" }, 400);
  const agents =
    body.agents && body.agents.length
      ? body.agents
      : ["agent-1", "agent-2", "agent-3"];

  const prov = await deps.forker.provision(body.base, agents);
  const secret = deps.newSecret();
  const run = createRun({
    id: deps.newId(),
    task: body.task ?? "",
    base: body.base,
    agents,
    at: deps.now(),
    idempotencyKey: key ?? undefined,
  });
  await deps.runs.create(run, secret);
  await deps.enqueue(run, prov, secret);
  const forks = prov.forks;

  const payload = {
    id: run.id,
    status: run.status,
    base: run.base,
    agents: run.agents,
    createdAt: run.createdAt,
    // The per-run secret the compute side signs events with. Sensitive: only the
    // creating caller (and the Workflow, via the store) ever sees it.
    eventSecret: secret,
    forks: forks.map((f) => ({ agent: f.agent, name: f.name, remote: f.remote })),
  };
  const bodyStr = JSON.stringify(payload, null, 2);
  await idem.commit(201, bodyStr);
  return new Response(bodyStr, { status: 201, headers: { "content-type": "application/json" } });
}

async function getRunHandler(req: Request, deps: Deps, id: string): Promise<Response> {
  const auth = await authorize(req, deps.tokens, "runs:read");
  if (!auth.ok) return json({ error: auth.error }, auth.status);
  const found = await deps.runs.get(id);
  if (!found) return json({ error: "run not found" }, 404);
  return json(publicRun(found.run));
}

async function actionHandler(
  req: Request,
  deps: Deps,
  id: string,
  action: RunAction,
  permission: Parameters<typeof authorize>[2],
): Promise<Response> {
  const auth = await authorize(req, deps.tokens, permission);
  if (!auth.ok) return json({ error: auth.error }, auth.status);
  const found = await deps.runs.get(id);
  if (!found) return json({ error: "run not found" }, 404);

  const res = applyAction(found.run, action, deps.now());
  if (!res.ok) return json({ error: res.error }, 409);
  await deps.runs.update(res.run);
  return json(publicRun(res.run));
}

/**
 * ledgerHandler returns a run's signed provenance chain (Phase 4b). Read-only
 * (runs:read). The records carry their own hashes + signatures, so a client can
 * verify the chain offline with `braid ledger verify` and the public key.
 */
async function ledgerHandler(req: Request, deps: Deps, id: string): Promise<Response> {
  const auth = await authorize(req, deps.tokens, "runs:read");
  if (!auth.ok) return json({ error: auth.error }, auth.status);
  if (!deps.ledger) return json({ error: "ledger not configured" }, 501);
  const records = await deps.ledger.list(id);
  return json(records);
}

/** listRunsHandler returns run summaries (newest first) for the console. */
async function listRunsHandler(req: Request, deps: Deps, url: URL): Promise<Response> {
  const auth = await authorize(req, deps.tokens, "runs:read");
  if (!auth.ok) return json({ error: auth.error }, auth.status);
  const limit = Math.min(Math.max(Number(url.searchParams.get("limit") ?? "50") || 50, 1), 200);
  const runs = await deps.runs.list(limit);
  return json({ runs });
}

/**
 * projectionHandler proxies an authenticated read of a run's live state / event
 * log / rebuilt state from the Orchestrator DO (Phase 4c). It first confirms the
 * run exists (404 otherwise), so the console never leaks the DO surface for an
 * unknown id.
 */
async function projectionHandler(
  req: Request,
  deps: Deps,
  id: string,
  kind: "state" | "events" | "rebuild",
  url: URL,
): Promise<Response> {
  const auth = await authorize(req, deps.tokens, "runs:read");
  if (!auth.ok) return json({ error: auth.error }, auth.status);
  if (!deps.projection) return json({ error: "projection not configured" }, 501);
  const found = await deps.runs.get(id);
  if (!found) return json({ error: "run not found" }, 404);
  if (kind === "events") {
    const since = Number(url.searchParams.get("since") ?? "0") || 0;
    return json(await deps.projection.events(id, since));
  }
  if (kind === "rebuild") return json(await deps.projection.rebuild(id));
  const state = await deps.projection.state(id);
  return json(state ?? {});
}

/**
 * costHandler publishes the per-run $/run estimate + margin (Phase 5c). It derives
 * conservative resource facts from the run's wall time, swarm size and projection
 * (conflict resolutions ≈ AI calls, event count ≈ requests), then prices them
 * against the published per-run price. The gate requires margin > 0.
 */
async function costHandler(req: Request, deps: Deps, id: string): Promise<Response> {
  const auth = await authorize(req, deps.tokens, "runs:read");
  if (!auth.ok) return json({ error: auth.error }, auth.status);
  const found = await deps.runs.get(id);
  if (!found) return json({ error: "run not found" }, 404);
  const run = found.run;

  let aiCalls = 0;
  let events = 0;
  let publishedBytes = 1024 * 256; // default assumption: a small merged tree
  if (deps.projection) {
    const st = await deps.projection.state(id);
    if (st) {
      aiCalls = Array.isArray(st.conflicts) ? st.conflicts.length : 0;
      events = typeof st.seq === "number" ? st.seq : (st.log?.length ?? 0);
      const merged = st.result?.summary?.["bytes"];
      if (typeof merged === "number" && merged > 0) publishedBytes = merged;
    }
  }
  const wallMs = Date.parse(run.updatedAt) - Date.parse(run.createdAt);
  const facts = factsFromRun({
    wallMs: Number.isFinite(wallMs) ? wallMs : 0,
    agents: run.agents.length,
    aiCalls,
    // requests: control (create/read) + ~events DO writes + WF steps; events is a good proxy.
    requests: Math.max(events, 1) + run.agents.length * 3 + 6,
    r2Writes: 3 + aiCalls, // ledger records (snapshot/verify/publish) + any blob offloads
    publishedBytes,
  });
  const priceUsd = deps.runPriceUsd ?? 0.1;
  const estimate: CostEstimate = estimateCost(facts, priceUsd);
  return json({ runId: id, facts, ...estimate });
}

/** publicRun is the run view returned to clients (no secret). */
function publicRun(run: Run) {
  return {
    id: run.id,
    status: run.status,
    task: run.task,
    base: run.base,
    agents: run.agents,
    createdAt: run.createdAt,
    updatedAt: run.updatedAt,
    verdict: run.verdict,
    mergedSha: run.mergedSha,
    history: run.history,
  };
}

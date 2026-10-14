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

/** A forked agent repo, as the control plane provisions it. */
export interface Fork {
  agent: string;
  name: string;
  remote: string;
  token: string;
  expiresAt?: string;
}

/** Forker provisions one fork + write token per agent off a seeded base. */
export interface Forker {
  fork(base: string, agents: string[]): Promise<Fork[]>;
}

/** RunsStore persists run records and their per-run HMAC secret. */
export interface RunsStore {
  create(run: Run, secret: string): Promise<void>;
  get(id: string): Promise<{ run: Run; secret: string } | null>;
  update(run: Run): Promise<void>;
}

export interface Deps {
  tokens: TokenStore;
  idem: IdempotencyStore;
  runs: RunsStore;
  forker: Forker;
  /** Enqueue the run for the Workflow consumer (Phase 3c); a no-op is fine. */
  enqueue: (run: Run, forks: Fork[]) => Promise<void>;
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

  const m = url.pathname.match(/^\/v1\/runs\/([A-Za-z0-9_-]+)(?:\/(cancel|approve|reject))?$/);
  if (m) {
    const id = m[1];
    const verb = m[2];
    if (!verb && req.method === "GET") return getRunHandler(req, deps, id);
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

  const forks = await deps.forker.fork(body.base, agents);
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
  await deps.enqueue(run, forks);

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

/**
 * Braid control plane (stage A).
 *
 * Proves the Artifacts primitives Braid's swarm is built on:
 *   POST /api/provision { base, forks }  -> create a base repo, fork it N times,
 *                                           mint a write token per fork.
 *
 * The orchestrator Durable Object and the agent loop build on top of this.
 */

import { handleV1 } from "./v1/router";
import { MapTokenStore, type Principal, type Role } from "./v1/auth";
import { ArtifactsForker, DOIdempotencyStore, DORunsStore } from "./v1/stores";
import { newSecret as newRunSecret, verify as verifyHmac, SIGNATURE_HEADER } from "./v1/hmac";
import type { Run } from "./v1/lifecycle";
import type { IntegrationJob } from "./v1/integration";
import { R2LedgerStore, type R2Like } from "./v1/ledger";
import {
  emptyState,
  reduce,
  rebuild,
  type BraidEvent,
  type EventMeta,
  type LoggedEvent,
  type RunState,
} from "./v1/runstate";

// Phase 3c classes the runtime instantiates (Containers + the durable consumer).
export { ComputeZone, IOZone } from "./containers";
export { IntegrationWorkflow } from "./v1/workflow";
// Phase 4a: per-base lease + parent-SHA CAS coordinator.
export { BaseCoordinator } from "./v1/coordinator-do";

interface Env {
  ARTIFACTS: ArtifactsNamespace;
  AI: { run(model: string, input: unknown): Promise<{ response?: string } & Record<string, unknown>> };
  ORCHESTRATOR: DurableObjectNamespace;
  // Phase 3c bindings: the two compute Containers (DO-backed) + the durable
  // Workflow that drives one run's two-zone pipeline.
  COMPUTE_ZONE: DurableObjectNamespace;
  IO_ZONE: DurableObjectNamespace;
  BASE_COORDINATOR: DurableObjectNamespace;
  INTEGRATION_WORKFLOW: Workflow;
  // Phase 4b: R2 bucket holding the signed provenance ledger (read side).
  LEDGER?: R2Bucket;
  LEDGER_SIGNING_SEED?: string;
  // CONTROL_TOKENS is a JSON map of bearer token -> { id, roles[] } for the /v1
  // control plane (a Worker secret in prod). Absent -> /v1 has no valid tokens,
  // so every /v1 request is 401 (fail closed), never open.
  CONTROL_TOKENS?: string;
}

// Workers AI model used to reconcile a true conflict hunk. Overridable; a
// larger instruct model gives better merges. Correctness is still decided by
// the integrator re-running the real test suite — the model never has final say.
const RESOLVE_MODEL = "@cf/meta/llama-3.3-70b-instruct-fp8-fast";

// Minimal shapes for the Artifacts binding (authoritative types come from
// `wrangler types`; these keep the control plane readable and typed).
interface ArtifactsCreateRepoResult {
  name: string;
  remote: string;
  defaultBranch: string;
  token: string;
}
interface ArtifactsCreateTokenResult {
  plaintext: string;
  expiresAt: string;
}
interface ArtifactsRepo extends Disposable {
  info(): Promise<unknown>;
  createToken(scope?: "read" | "write", ttl?: number): Promise<ArtifactsCreateTokenResult>;
  fork(name: string, opts?: { description?: string; readOnly?: boolean; defaultBranchOnly?: boolean }): Promise<ArtifactsCreateRepoResult>;
  log(opts?: { ref?: string; limit?: number }): Promise<unknown>;
  readFile(args: { ref: string; path: string }): Promise<Blob | null>;
}
interface ArtifactsNamespace {
  create(name: string, opts?: { readOnly?: boolean; description?: string; setDefaultBranch?: string }): Promise<ArtifactsCreateRepoResult>;
  get(name: string): Promise<ArtifactsRepo>;
  list(opts?: { limit?: number; cursor?: string }): Promise<unknown>;
  delete(name: string): Promise<boolean>;
}

const json = (data: unknown, status = 200) =>
  new Response(JSON.stringify(data, null, 2), {
    status,
    headers: { "content-type": "application/json" },
  });

/**
 * tokenStore builds the /v1 token store from the CONTROL_TOKENS secret (a JSON
 * map token -> { id, roles }). A missing/invalid secret yields an empty store,
 * so /v1 fails closed (every request 401) rather than open.
 */
function tokenStore(env: Env): MapTokenStore {
  const m = new Map<string, Principal>();
  try {
    if (env.CONTROL_TOKENS) {
      const parsed = JSON.parse(env.CONTROL_TOKENS) as Record<string, { id: string; roles: Role[] }>;
      for (const [token, p] of Object.entries(parsed)) {
        if (token && p?.id && Array.isArray(p.roles)) m.set(token, { id: p.id, roles: p.roles });
      }
    }
  } catch {
    /* malformed secret -> empty store (fail closed) */
  }
  return new MapTokenStore(m);
}

export default {
  async fetch(req: Request, env: Env): Promise<Response> {
    const url = new URL(req.url);

    if (url.pathname === "/api" || url.pathname === "/api/health") {
      return json({
        service: "braid-worker",
        ok: true,
        endpoints: [
          "POST /v1/runs", "GET /v1/runs/:id", "POST /v1/runs/:id/{cancel,approve,reject}",
          "/api/base", "/api/fork", "/api/resolve", "/runs/:id/event", "/runs/:id/ws", "/runs/:id/state",
        ],
      });
    }

    // The authenticated /v1 control plane. handleV1 returns null for non-/v1
    // paths, so legacy routes below still work.
    const v1 = await handleV1(req, {
      tokens: tokenStore(env),
      idem: new DOIdempotencyStore(env.ORCHESTRATOR),
      runs: new DORunsStore(env.ORCHESTRATOR),
      forker: new ArtifactsForker(env.ARTIFACTS as unknown as ConstructorParameters<typeof ArtifactsForker>[0]),
      // Phase 3c: start the durable Workflow that drives this run's two-zone
      // pipeline (io snapshot -> compute integrate -> io CAS-publish). The job
      // carries the provisioned remotes + scoped tokens and the per-run HMAC
      // secret; tokens are handed only to the io zone inside the Workflow.
      enqueue: async (run, prov, secret) => {
        const job: IntegrationJob = {
          runId: run.id,
          baseKey: run.base,
          task: run.task,
          base: {
            remote: prov.base.remote,
            branch: prov.base.branch,
            token: { plaintext: prov.base.token, expiresAt: prov.base.expiresAt },
          },
          agents: prov.forks.map((f) => ({
            id: f.agent,
            remote: f.remote,
            branch: "main",
            token: { plaintext: f.token, expiresAt: f.expiresAt },
          })),
          eventSecret: secret,
        };
        await env.INTEGRATION_WORKFLOW.create({ id: run.id, params: job });
      },
      ledger: env.LEDGER ? new R2LedgerStore(env.LEDGER as unknown as R2Like) : undefined,
      now: () => new Date().toISOString(),
      newId: () => `run-${Date.now().toString(36)}-${Math.random().toString(36).slice(2, 8)}`,
      newSecret: newRunSecret,
    });
    if (v1) return v1;

    // Live run routes are handled by the per-run orchestrator Durable Object.
    // /runs/<id>/{ws,event,state} — the dashboard subscribes over WS; the
    // pipeline (local demo or live swarm) POSTs structured events to /event.
    const runMatch = url.pathname.match(/^\/runs\/([A-Za-z0-9_-]+)(\/.*)?$/);
    if (runMatch) {
      const runId = runMatch[1];
      const id = env.ORCHESTRATOR.idFromName(runId);
      const stub = env.ORCHESTRATOR.get(id);
      return stub.fetch(req);
    }

    // Step 1: create an empty base repo + a write token so the caller can seed
    // it with the baseline app before forking.
    if (url.pathname === "/api/base" && req.method === "POST") {
      const body = (await req.json().catch(() => ({}))) as { name?: string };
      const name = body.name ?? `braid-base-${Date.now().toString(36)}`;
      const baseRepo = await env.ARTIFACTS.create(name, {
        description: "Braid baseline (control-plane provisioned)",
      });
      const tok = await (await env.ARTIFACTS.get(name)).createToken("write", 3600);
      return json({
        name: baseRepo.name,
        remote: baseRepo.remote,
        defaultBranch: baseRepo.defaultBranch,
        token: tok.plaintext,
        expiresAt: tok.expiresAt,
      });
    }

    // Step 2: fork the (now seeded) base once per agent, each with a write token.
    if (url.pathname === "/api/fork" && req.method === "POST") {
      const body = (await req.json().catch(() => ({}))) as { base?: string; agents?: string[]; count?: number };
      if (!body.base) return json({ error: "base required" }, 400);
      const agents =
        body.agents ??
        Array.from({ length: Math.min(Math.max(body.count ?? 3, 1), 8) }, (_, i) => `agent-${i + 1}`);

      const forks: { agent: string; name: string; remote: string; token: string; expiresAt: string }[] = [];
      using handle = await env.ARTIFACTS.get(body.base);
      for (const agent of agents) {
        const forkName = `${body.base}-${agent}`;
        const forked = await handle.fork(forkName, { defaultBranchOnly: true });
        const tok = await (await env.ARTIFACTS.get(forkName)).createToken("write", 3600);
        forks.push({ agent, name: forkName, remote: forked.remote, token: tok.plaintext, expiresAt: tok.expiresAt });
      }
      return json({ base: body.base, forks });
    }

    // LLM conflict reconciliation via Workers AI (no Anthropic key needed).
    // The integrator calls this only for a true conflict hunk git couldn't
    // auto-merge, then re-verifies the result by running the tests.
    if (url.pathname === "/api/resolve" && req.method === "POST") {
      const h = (await req.json().catch(() => ({}))) as {
        path?: string; oursIntent?: string; theirsIntent?: string;
        base?: string; ours?: string; theirs?: string;
        contextBefore?: string; contextAfter?: string; lastFailure?: string;
      };
      const prompt = [
        `Resolve a git merge conflict in \`${h.path}\`. Two agents edited the same region:`,
        `- "ours" implements: ${h.oursIntent}`,
        `- "theirs" implements: ${h.theirsIntent}`,
        ``,
        `Common ancestor (base):\n${h.base ?? ""}`,
        `Ours:\n${h.ours ?? ""}`,
        `Theirs:\n${h.theirs ?? ""}`,
        `Context before:\n${h.contextBefore ?? ""}`,
        `Context after:\n${h.contextAfter ?? ""}`,
        h.lastFailure ? `\nPrevious attempt FAILED tests:\n${h.lastFailure}\nFix it.` : "",
        ``,
        `Compose BOTH intents into one correct version. Preserve indentation.`,
        `Return ONLY the replacement code for the conflicted region — no markers, no fences, no prose.`,
      ].join("\n");

      const out = await env.AI.run(RESOLVE_MODEL, {
        messages: [
          { role: "system", content: "You are a precise code-merge assistant. Output only code." },
          { role: "user", content: prompt },
        ],
        max_tokens: 1024,
      });
      const raw = (out.response ?? "").trim();
      const content = raw.replace(/^```[a-z]*\n?/i, "").replace(/\n?```$/i, "");
      return json({ model: RESOLVE_MODEL, content });
    }

    return json({ error: "not found" }, 404);
  },
} satisfies ExportedHandler<Env>;

// ───────────────────────────────────────────────────────────────────────────
// Orchestrator Durable Object — one instance per run id. Ingests the pipeline's
// structured events, reduces them into a single live state snapshot, persists
// it (so it survives hibernation + late joiners), and fans it out to every
// subscribed dashboard over WebSocket.
// ───────────────────────────────────────────────────────────────────────────

// Phase 4c tuning: offload an event body larger than this (bytes) to R2, keeping
// the DO log entry a small blob reference; snapshot the projection every N events
// so a rebuild can start from the latest checkpoint + tail.
const EVENT_BLOB_THRESHOLD = 8 * 1024;
const SNAPSHOT_EVERY = 50;
const seqKey = (seq: number) => `evt:${String(seq).padStart(9, "0")}`;

export class Orchestrator {
  private state!: RunState;
  private lastSeq = 0;
  private loaded: Promise<void>;

  constructor(private ctx: DurableObjectState, private env: Env) {
    this.loaded = this.ctx.blockConcurrencyWhile(async () => {
      const stored = await this.ctx.storage.get<RunState>("state");
      this.state = stored ?? emptyState("run", Date.now());
      this.lastSeq = (await this.ctx.storage.get<number>("lastSeq")) ?? this.state.seq ?? 0;
    });
  }

  private broadcast() {
    const msg = JSON.stringify({ type: "state", state: this.state, seq: this.lastSeq });
    for (const ws of this.ctx.getWebSockets()) {
      try {
        ws.send(msg);
      } catch {
        /* socket closing */
      }
    }
  }

  /** readLog returns the run's event log from `since` (exclusive), rehydrating any
   *  R2-offloaded event bodies. The log is the authority the projection rebuilds from. */
  private async readLog(since = 0): Promise<LoggedEvent[]> {
    const start = seqKey(since + 1);
    const map = await this.ctx.storage.list<LoggedEvent | { meta: EventMeta; blobKey: string }>({
      prefix: "evt:",
      start,
    });
    const out: LoggedEvent[] = [];
    for (const entry of map.values()) {
      if ("blobKey" in entry) {
        const body = this.env.LEDGER ? await this.env.LEDGER.get(entry.blobKey) : null;
        out.push({ meta: entry.meta, event: body ? (JSON.parse(await body.text()) as BraidEvent) : { type: "log", line: "[event body unavailable]" } });
      } else {
        out.push(entry);
      }
    }
    return out;
  }

  /** append persists one event under its seq, offloading an oversized body to R2
   *  so the DO entry stays small, and checkpoints the projection periodically. */
  private async append(seq: number, event: BraidEvent, meta: EventMeta): Promise<void> {
    const body = JSON.stringify(event);
    if (body.length > EVENT_BLOB_THRESHOLD && this.env.LEDGER) {
      const blobKey = `events/${this.state.runId}/${String(seq).padStart(9, "0")}.json`;
      await this.env.LEDGER.put(blobKey, body);
      await this.ctx.storage.put(seqKey(seq), { meta, blobKey });
    } else {
      await this.ctx.storage.put(seqKey(seq), { meta, event } satisfies LoggedEvent);
    }
    await this.ctx.storage.put("lastSeq", seq);
    await this.ctx.storage.put("state", this.state);
    if (seq % SNAPSHOT_EVERY === 0) {
      await this.ctx.storage.put("snapshot", this.state);
    }
  }

  async fetch(req: Request): Promise<Response> {
    await this.loaded;
    const url = new URL(req.url);

    // Idempotency store (only used on the well-known control instance).
    const idem = url.pathname.match(/^\/idem\/(.+)$/);
    if (idem) {
      const key = `idem:${decodeURIComponent(idem[1])}`;
      if (req.method === "PUT") {
        await this.ctx.storage.put(key, await req.json());
        return json({ ok: true });
      }
      const rec = await this.ctx.storage.get(key);
      return rec ? json(rec) : json({ error: "not found" }, 404);
    }

    const m = url.pathname.match(/^\/runs\/([A-Za-z0-9_-]+)(\/.*)?$/);
    const runId = m?.[1] ?? "run";
    const sub = m?.[2] ?? "";
    if (this.state.runId === "run") this.state.runId = runId;

    // Control-plane: seed this run's lifecycle record + per-run HMAC secret.
    if (sub === "/init" && req.method === "POST") {
      const { run, secret } = (await req.json()) as { run: Run; secret: string };
      await this.ctx.storage.put("run", run);
      await this.ctx.storage.put("secret", secret);
      if (this.state.task === "") this.state.task = run.task;
      await this.ctx.storage.put("state", this.state);
      return json({ ok: true });
    }

    // Control-plane: read / update the lifecycle record (cancel/approve go here).
    if (sub === "/record") {
      if (req.method === "PUT") {
        await this.ctx.storage.put("run", await req.json());
        return json({ ok: true });
      }
      const run = await this.ctx.storage.get<Run>("run");
      const secret = await this.ctx.storage.get<string>("secret");
      if (!run) return json({ error: "run not found" }, 404);
      return json({ run, secret });
    }

    // Control-plane: record a SHA Braid itself just published, so a later inbound
    // change event naming it can be dropped instead of spawning a run from our own
    // merge (C9 self-trigger guard). The external Artifacts webhook that would feed
    // such events is not wired here yet; this persists the guard's state for it.
    if (sub === "/published" && req.method === "POST") {
      const { sha } = (await req.json().catch(() => ({}))) as { sha?: string };
      if (sha) {
        const published = (await this.ctx.storage.get<string[]>("published")) ?? [];
        if (!published.includes(sha)) {
          published.push(sha);
          await this.ctx.storage.put("published", published.slice(-50));
        }
      }
      return json({ ok: true });
    }
    if (sub === "/published") {
      return json({ published: (await this.ctx.storage.get<string[]>("published")) ?? [] });
    }

    // Dashboard subscribes here.
    if (sub === "/ws") {
      if (req.headers.get("Upgrade") !== "websocket") {
        return new Response("expected websocket", { status: 426 });
      }
      const pair = new WebSocketPair();
      const [client, server] = Object.values(pair);
      this.ctx.acceptWebSocket(server);
      server.send(JSON.stringify({ type: "state", state: this.state }));
      return new Response(null, { status: 101, webSocket: client });
    }

    // Pipeline posts structured events here. If this run was created through the
    // /v1 control plane it has a per-run HMAC secret, and every event must carry
    // a matching x-braid-signature over the raw body (C10). A run with no secret
    // (the legacy demo path) is accepted unsigned, preserving today's behavior.
    if (sub === "/event" && req.method === "POST") {
      const raw = await req.text();
      const secret = await this.ctx.storage.get<string>("secret");
      if (secret) {
        const ok = await verifyHmac(secret, raw, req.headers.get(SIGNATURE_HEADER));
        if (!ok) return json({ error: "invalid or missing event signature" }, 401);
      }
      let event: BraidEvent | null = null;
      try {
        event = raw ? (JSON.parse(raw) as BraidEvent) : null;
      } catch {
        event = null;
      }
      if (!event || typeof event.type !== "string") {
        return json({ error: "invalid event" }, 400);
      }
      // Assign a monotonic seq + receive timestamp, fold deterministically, and
      // append to the seq-log (the rebuildable source; the projection is a cache).
      const seq = ++this.lastSeq;
      const meta: EventMeta = { seq, ts: Date.now() };
      reduce(this.state, event, meta);
      await this.append(seq, event, meta);
      this.broadcast();
      return json({ ok: true, seq });
    }

    // Snapshot for polling / debugging.
    if (sub === "/state") {
      return json(this.state);
    }

    // The seq-numbered event log from ?since=<seq> (exclusive) — for resume + audit.
    if (sub === "/events") {
      const since = Number(url.searchParams.get("since") ?? "0") || 0;
      return json({ runId, lastSeq: this.lastSeq, events: await this.readLog(since) });
    }

    // Rebuild the projection from the persisted log (resuming from the snapshot if
    // present) and return it — proof the state is reconstructable from events.
    if (sub === "/rebuild") {
      const snapshot = await this.ctx.storage.get<RunState>("snapshot");
      const since = snapshot?.seq ?? 0;
      const rebuilt = rebuild(runId, await this.readLog(since), snapshot ?? undefined);
      return json(rebuilt);
    }

    // Wipe a run (demo reset): clear the projection AND the seq-log.
    if (sub === "/reset" && req.method === "POST") {
      this.state = emptyState(runId, Date.now());
      this.lastSeq = 0;
      await this.ctx.storage.deleteAll();
      await this.ctx.storage.put("state", this.state);
      await this.ctx.storage.put("lastSeq", 0);
      this.broadcast();
      return json({ ok: true });
    }

    return json({ error: "not found" }, 404);
  }

  async webSocketMessage(ws: WebSocket, message: string | ArrayBuffer) {
    // Dashboards are read-only; a "sync" ping just re-sends current state.
    if (typeof message === "string" && message === "sync") {
      ws.send(JSON.stringify({ type: "state", state: this.state }));
    }
  }

  async webSocketClose(ws: WebSocket, code: number) {
    try {
      ws.close(code);
    } catch {
      /* already closed */
    }
  }
}

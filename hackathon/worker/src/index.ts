/**
 * Braid control plane (stage A).
 *
 * Proves the Artifacts primitives Braid's swarm is built on:
 *   POST /api/provision { base, forks }  -> create a base repo, fork it N times,
 *                                           mint a write token per fork.
 *
 * The orchestrator Durable Object and the agent loop build on top of this.
 */

interface Env {
  ARTIFACTS: ArtifactsNamespace;
  AI: { run(model: string, input: unknown): Promise<{ response?: string } & Record<string, unknown>> };
  ORCHESTRATOR: DurableObjectNamespace;
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

export default {
  async fetch(req: Request, env: Env): Promise<Response> {
    const url = new URL(req.url);

    if (url.pathname === "/api" || url.pathname === "/api/health") {
      return json({
        service: "braid-worker",
        ok: true,
        endpoints: ["/api/base", "/api/fork", "/api/resolve", "/runs/:id/event", "/runs/:id/ws", "/runs/:id/state"],
      });
    }

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

type BraidEvent = Record<string, any> & { type: string };

interface AgentView {
  id: string;
  branch: string;
  intent: string;
  status: string; // spawned | forked | coding | pushed
  merge: string; // waiting | merging | auto-merged | conflict | resolved | verified
  files: string[];
  test: string;
}

interface RunState {
  runId: string;
  task: string;
  mode: string;
  startedAt: number;
  updatedAt: number;
  phase: string; // init | swarming | integrating | done
  base: { name: string; remote: string } | null;
  agents: Record<string, AgentView>;
  agentOrder: string[];
  conflicts: {
    agent: string;
    path: string;
    strategy: string;
    attempts: number;
    verified: string;
    status: string; // resolving | resolved
  }[];
  result: { green: boolean; finalTest: string; mergedSha: string; summary: Record<string, number>; usersFile?: string } | null;
  log: { ts: number; line: string }[];
}

function emptyState(runId: string): RunState {
  return {
    runId,
    task: "",
    mode: "",
    startedAt: Date.now(),
    updatedAt: Date.now(),
    phase: "init",
    base: null,
    agents: {},
    agentOrder: [],
    conflicts: [],
    result: null,
    log: [],
  };
}

function pushLog(s: RunState, line: string) {
  s.log.push({ ts: Date.now(), line });
  if (s.log.length > 200) s.log.shift();
}

/** Pure reducer: fold one event into the run state. */
function reduce(s: RunState, e: BraidEvent): RunState {
  s.updatedAt = Date.now();
  switch (e.type) {
    case "run:init": {
      s.task = e.task;
      s.mode = e.mode;
      s.phase = "swarming";
      for (const a of e.agents as { id: string; branch: string; intent: string }[]) {
        s.agents[a.id] = { id: a.id, branch: a.branch, intent: a.intent, status: "spawned", merge: "waiting", files: [], test: "" };
        if (!s.agentOrder.includes(a.id)) s.agentOrder.push(a.id);
      }
      pushLog(s, `run started — ${e.agents.length} agents · ${e.mode}`);
      break;
    }
    case "base:ready":
      s.base = { name: e.name, remote: e.remote };
      pushLog(s, `base repo ready: ${e.name}`);
      break;
    case "agent:status": {
      const a = s.agents[e.agent];
      if (a) a.status = e.status;
      pushLog(s, `${e.agent}: ${e.status}${e.detail ? ` — ${e.detail}` : ""}`);
      break;
    }
    case "merge:start": {
      s.phase = "integrating";
      const a = s.agents[e.agent];
      if (a) a.merge = "merging";
      pushLog(s, `integrating ${e.agent}…`);
      break;
    }
    case "merge:auto": {
      const a = s.agents[e.agent];
      if (a) {
        a.merge = "auto-merged";
        a.files = e.files;
      }
      pushLog(s, `${e.agent}: git auto-merged ${e.files.length} file(s) — no AI`);
      break;
    }
    case "conflict:detected": {
      const a = s.agents[e.agent];
      if (a) a.merge = "conflict";
      for (const path of e.files as string[]) {
        if (!s.conflicts.find((c) => c.agent === e.agent && c.path === path)) {
          s.conflicts.push({ agent: e.agent, path, strategy: "?", attempts: 0, verified: "", status: "resolving" });
        }
      }
      pushLog(s, `${e.agent}: ⚡ conflict in ${(e.files as string[]).join(", ")}`);
      break;
    }
    case "conflict:attempt": {
      const c = s.conflicts.find((c) => c.agent === e.agent && c.path === e.path);
      if (c) {
        c.strategy = e.strategy;
        c.attempts = e.attempt;
        c.status = "resolving";
      }
      pushLog(s, `${e.agent}: resolving ${e.path} via ${e.strategy} (attempt ${e.attempt})`);
      break;
    }
    case "conflict:resolved": {
      const c = s.conflicts.find((c) => c.agent === e.agent && c.path === e.path);
      if (c) {
        c.strategy = e.strategy;
        c.attempts = e.attempts;
        c.verified = e.verified;
        c.status = "resolved";
      }
      const a = s.agents[e.agent];
      if (a) a.merge = "resolved";
      pushLog(s, `${e.agent}: ✓ ${e.path} resolved via ${e.strategy} — ${e.verified}`);
      break;
    }
    case "verify": {
      if (e.scope !== "final") {
        const a = s.agents[e.scope];
        if (a) {
          a.test = e.summary;
          if (e.passed && a.merge !== "auto-merged") a.merge = "verified";
        }
      }
      pushLog(s, `${e.scope === "final" ? "final" : e.scope} tests: ${e.passed ? "✅" : "❌"} ${e.summary}`);
      break;
    }
    case "run:done":
      s.phase = "done";
      s.result = { green: e.green, finalTest: e.finalTest, mergedSha: e.mergedSha, summary: e.summary, usersFile: e.usersFile };
      pushLog(s, `run ${e.green ? "GREEN ✅" : "RED ❌"} — ${e.finalTest}`);
      break;
    case "log":
      pushLog(s, e.line);
      break;
  }
  return s;
}

export class Orchestrator {
  private state!: RunState;
  private loaded: Promise<void>;

  constructor(private ctx: DurableObjectState, private env: Env) {
    this.loaded = this.ctx.blockConcurrencyWhile(async () => {
      const stored = await this.ctx.storage.get<RunState>("state");
      this.state = stored ?? emptyState("run");
    });
  }

  private broadcast() {
    const msg = JSON.stringify({ type: "state", state: this.state });
    for (const ws of this.ctx.getWebSockets()) {
      try {
        ws.send(msg);
      } catch {
        /* socket closing */
      }
    }
  }

  async fetch(req: Request): Promise<Response> {
    await this.loaded;
    const url = new URL(req.url);
    const m = url.pathname.match(/^\/runs\/([A-Za-z0-9_-]+)(\/.*)?$/);
    const runId = m?.[1] ?? "run";
    const sub = m?.[2] ?? "";
    if (this.state.runId === "run") this.state.runId = runId;

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

    // Pipeline posts structured events here.
    if (sub === "/event" && req.method === "POST") {
      const event = (await req.json().catch(() => null)) as BraidEvent | null;
      if (!event || typeof event.type !== "string") {
        return json({ error: "invalid event" }, 400);
      }
      reduce(this.state, event);
      await this.ctx.storage.put("state", this.state);
      this.broadcast();
      return json({ ok: true });
    }

    // Snapshot for polling / debugging.
    if (sub === "/state") {
      return json(this.state);
    }

    // Wipe a run (demo reset).
    if (sub === "/reset" && req.method === "POST") {
      this.state = emptyState(runId);
      await this.ctx.storage.put("state", this.state);
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

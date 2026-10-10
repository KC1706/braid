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

    if (url.pathname === "/") {
      return json({ service: "braid-worker", ok: true, endpoints: ["/api/base", "/api/fork", "/api/resolve"] });
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

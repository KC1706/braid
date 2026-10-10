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
}

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
      return json({ service: "braid-worker", ok: true, endpoints: ["/api/base", "/api/fork"] });
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

    return json({ error: "not found" }, 404);
  },
} satisfies ExportedHandler<Env>;

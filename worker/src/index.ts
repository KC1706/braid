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
      return json({ service: "braid-worker", ok: true, endpoints: ["/api/provision"] });
    }

    // Create a base repo + N forks, each with a write token — the swarm's substrate.
    if (url.pathname === "/api/provision" && req.method === "POST") {
      const body = (await req.json().catch(() => ({}))) as { base?: string; forks?: number };
      const run = Date.now().toString(36);
      const base = body.base ?? `braid-base-${run}`;
      const n = Math.min(Math.max(body.forks ?? 3, 1), 8);

      const baseRepo = await env.ARTIFACTS.create(base, {
        description: "Braid baseline (control-plane provisioned)",
      });

      const forks: { agent: string; name: string; remote: string; token: string; expiresAt: string }[] = [];
      using handle = await env.ARTIFACTS.get(base);
      for (let i = 1; i <= n; i++) {
        const forkName = `${base}-agent-${i}`;
        const forked = await handle.fork(forkName, { defaultBranchOnly: true });
        const tok = await (await env.ARTIFACTS.get(forkName)).createToken("write", 3600);
        forks.push({
          agent: `agent-${i}`,
          name: forkName,
          remote: forked.remote,
          token: tok.plaintext,
          expiresAt: tok.expiresAt,
        });
      }

      return json({
        run,
        base: { name: baseRepo.name, remote: baseRepo.remote, defaultBranch: baseRepo.defaultBranch },
        forks,
      });
    }

    return json({ error: "not found" }, 404);
  },
} satisfies ExportedHandler<Env>;

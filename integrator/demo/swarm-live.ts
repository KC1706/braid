import { fileURLToPath } from "node:url";
import { dirname, join, resolve } from "node:path";
import { mkdirSync, rmSync, writeFileSync, readFileSync } from "node:fs";
import { gitOk, git } from "../src/git.js";
import { integrate } from "../src/integrate.js";
import { UnionResolver, ClaudeResolver, HybridResolver } from "../src/resolver.js";
import { FILES_BASELINE, AGENT_EDITS } from "./seed.js";

/**
 * END-TO-END SWARM ON REAL CLOUDFLARE ARTIFACTS.
 *
 * Uses the deployed control-plane Worker to create + fork real Artifacts repos,
 * drives three "agents" that push divergent branches to their forks, then runs
 * Braid's merge engine (the same code proven in the local demo) to compose them
 * into a green merge and push it back to the base repo's main.
 *
 * This orchestrator is the stand-in for what will run inside a Sandbox / agent
 * Workers — the git + merge + test logic is identical.
 */

const __dirname = dirname(fileURLToPath(import.meta.url));
const repoRoot = resolve(__dirname, "../..");
const nodeModules = join(repoRoot, "node_modules");
const tmp = resolve(__dirname, "../.tmp/live");

const WORKER = process.env.BRAID_WORKER_URL ?? "https://braid-worker.kunalchandoa925.workers.dev";
const AGENTS = ["agent-1", "agent-2", "agent-3"];

function banner(s: string) {
  console.log(`\n\x1b[1m\x1b[36m${s}\x1b[0m`);
}

/** Embed the token secret as HTTP basic-auth so plain `git` can push/fetch. */
function authRemote(remote: string, token: string): string {
  const secret = token.split("?expires=")[0];
  return `https://x:${secret}@${remote.slice("https://".length)}`;
}

async function post(path: string, body: unknown): Promise<any> {
  const res = await fetch(`${WORKER}${path}`, {
    method: "POST",
    headers: { "content-type": "application/json" },
    body: JSON.stringify(body),
  });
  if (!res.ok) throw new Error(`${path} -> ${res.status}: ${await res.text()}`);
  return res.json();
}

async function initCommitPush(dir: string, files: Record<string, string>, remote: string, msg: string) {
  mkdirSync(dir, { recursive: true });
  await gitOk(dir, ["init", "-q", "-b", "main"]);
  await gitOk(dir, ["config", "user.email", "braid@example.com"]);
  await gitOk(dir, ["config", "user.name", "Braid"]);
  for (const [p, c] of Object.entries(files)) {
    mkdirSync(join(dir, p, ".."), { recursive: true });
    writeFileSync(join(dir, p), c);
  }
  await gitOk(dir, ["add", "-A"]);
  await gitOk(dir, ["commit", "-q", "-m", msg]);
  await gitOk(dir, ["push", remote, "HEAD:main"]);
}

async function main() {
  rmSync(tmp, { recursive: true, force: true });
  mkdirSync(tmp, { recursive: true });
  const run = Date.now().toString(36);

  banner(`① Creating a real Artifacts base repo via ${WORKER}…`);
  const base = await post("/api/base", { name: `braid-live-${run}` });
  console.log(`   base repo: ${base.name}`);
  console.log(`   remote   : ${base.remote}`);

  banner("② Seeding the base repo with the Hono baseline app (real git push)…");
  await initCommitPush(join(tmp, "base-seed"), FILES_BASELINE, authRemote(base.remote, base.token), "baseline: Hono /users API");
  console.log("   ✓ baseline pushed to base/main");

  banner("③ Forking the base once per agent (real Artifacts forks)…");
  const { forks } = await post("/api/fork", { base: base.name, agents: AGENTS });
  for (const f of forks) console.log(`   • ${f.agent} → ${f.name}`);

  banner("④ Each agent edits its fork and pushes (2 collide on getUser)…");
  for (const f of forks) {
    const edit = AGENT_EDITS[f.agent];
    const dir = join(tmp, f.agent);
    await gitOk(tmp, ["clone", "-q", authRemote(f.remote, f.token), dir]);
    await gitOk(dir, ["config", "user.email", "braid@example.com"]);
    await gitOk(dir, ["config", "user.name", f.agent]);
    for (const [p, c] of Object.entries(edit.files)) {
      mkdirSync(join(dir, p, ".."), { recursive: true });
      writeFileSync(join(dir, p), c);
    }
    await gitOk(dir, ["add", "-A"]);
    await gitOk(dir, ["commit", "-q", "-m", `${f.agent}: ${edit.intent}`]);
    await gitOk(dir, ["push", "origin", "HEAD:main"]);
    console.log(`   ✓ ${f.agent} pushed "${edit.intent}" to its fork`);
  }

  banner("⑤ Integrator: clone base, fetch every fork, merge + re-test…");
  const integ = join(tmp, "integration");
  await gitOk(tmp, ["clone", "-q", authRemote(base.remote, base.token), integ]);
  for (const f of forks) {
    await gitOk(integ, ["remote", "add", f.agent, authRemote(f.remote, f.token)]);
    await gitOk(integ, ["fetch", "-q", f.agent]);
  }

  const claude = new ClaudeResolver();
  const resolver = new HybridResolver(new UnionResolver(), claude);
  const { trail, green } = await integrate({
    repoDir: integ,
    baseline: "main",
    task: "Add input validation and a rate limiter to the /users route.",
    agents: forks.map((f: any) => ({ id: f.agent, branch: `${f.agent}/main`, intent: AGENT_EDITS[f.agent].intent })),
    resolver,
    verifyOptions: { nodeModules },
    log: (m) => console.log(m),
  });

  banner("⑥ Pushing the merged, test-green result back to base/main…");
  await gitOk(integ, ["push", "origin", "HEAD:main"]);
  console.log("   ✓ merged result pushed");

  banner("⑦ Merged src/users.ts (both features survive, from the real base repo):");
  console.log(readFileSync(join(integ, "src/users.ts"), "utf8").trimEnd());

  banner("⑧ Result");
  console.log(`   base repo   : ${base.name}`);
  console.log(`   agents      : ${trail.summary.agentsMerged} merged`);
  console.log(`   auto-merged : ${trail.summary.filesAutoMerged} file(s) (no AI)`);
  console.log(`   conflicts   : ${trail.summary.conflictsResolved} resolved (${trail.summary.llmResolutions} via LLM)`);
  console.log(`   final test  : ${trail.finalTest}`);
  console.log(`   status      : ${green ? "\x1b[32mGREEN ✅\x1b[0m" : "\x1b[31mRED ❌\x1b[0m"}`);
  console.log(`\n   inspect: git -c http.extraHeader="Authorization: Bearer <token>" clone ${base.remote}`);
  if (!green) process.exit(1);
}

main().catch((err) => {
  console.error("\x1b[31mLive swarm failed:\x1b[0m", err);
  process.exit(1);
});

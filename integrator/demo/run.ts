import { fileURLToPath } from "node:url";
import { dirname, join, resolve } from "node:path";
import { readFileSync } from "node:fs";
import { seed } from "./seed.js";
import { integrate } from "../src/integrate.js";
import { UnionResolver, HybridResolver, autoLlmResolver } from "../src/resolver.js";
import { BraidEmitter } from "../src/events.js";

const __dirname = dirname(fileURLToPath(import.meta.url));
const repoRoot = resolve(__dirname, "../..");
const nodeModules = join(repoRoot, "node_modules");
const repoDir = resolve(__dirname, "../.tmp/demo-repo");

const TASK = "Add input validation and a rate limiter to the /users route.";

// Stream the run to the live dashboard (best-effort). The DO lives in the
// deployed worker, so the local demo lights up the dashboard with no cloud of
// its own — set BRAID_WORKER_URL="" to disable, or BRAID_DASHBOARD=off.
const WORKER = process.env.BRAID_DASHBOARD === "off"
  ? undefined
  : process.env.BRAID_WORKER_URL ?? "https://braid-worker.kunalchandoa925.workers.dev";
const runId = process.env.BRAID_RUN_ID ?? `demo-${Date.now().toString(36)}`;
const bus = new BraidEmitter(WORKER, runId);

function banner(s: string) {
  console.log(`\n\x1b[1m\x1b[36m${s}\x1b[0m`);
}

async function main() {
  banner("① Seeding baseline + 3 agent forks (2 collide on getUser)…");
  const { baseline, agents } = await seed(repoDir);
  for (const a of agents) console.log(`   • ${a.id} [${a.branch}] — ${a.intent}`);
  if (bus.dashboardUrl) banner(`📺 Live dashboard: ${bus.dashboardUrl}`);

  bus.emit({ type: "run:init", task: TASK, mode: "local", agents });
  // Local agents are seeded instantly — surface them as pushed so the lanes fill.
  for (const a of agents) {
    bus.emit({ type: "agent:status", agent: a.id, status: "coding", detail: a.intent });
    bus.emit({ type: "agent:status", agent: a.id, status: "pushed", detail: a.branch });
  }

  const llm = autoLlmResolver();
  const resolver = new HybridResolver(new UnionResolver(), llm);
  banner(
    `② Integrating with hybrid resolver (deterministic union → ` +
      `${llm ? llm.name + " on retry" : "union only (no LLM configured)"})…`,
  );

  const { mergedSha, trail, green, verdict } = await integrate({
    repoDir,
    baseline,
    task: TASK,
    agents,
    resolver,
    verifyOptions: { nodeModules },
    log: (m) => console.log(m),
    onEvent: (e) => bus.emit(e),
  });

  banner("③ Merged src/users.ts (both features survive):");
  const usersFile = readFileSync(join(repoDir, "src/users.ts"), "utf8").trimEnd();
  console.log(usersFile);

  bus.emit({
    type: "run:done",
    green,
    finalTest: trail.finalTest,
    mergedSha,
    summary: trail.summary,
    usersFile,
  });

  banner("④ .braid/trail.json — the WHY:");
  console.log(readFileSync(join(repoDir, ".braid/trail.json"), "utf8").trimEnd());

  banner("⑤ Result");
  console.log(`   merged sha : ${mergedSha.slice(0, 12)}`);
  console.log(`   agents     : ${trail.summary.agentsMerged} merged`);
  console.log(`   auto-merged: ${trail.summary.filesAutoMerged} file(s) (no AI)`);
  console.log(`   conflicts  : ${trail.summary.conflictsResolved} resolved ` +
    `(${trail.summary.llmResolutions} via LLM)`);
  console.log(`   final test : ${trail.finalTest}`);
  console.log(`   verdict    : ${verdict}`);
  if (trail.incompatibilities?.length) {
    console.log(`   incompat.  : ${trail.incompatibilities.length} (${trail.incompatibilities.map((i) => `${i.agent}:${i.action}`).join(", ")})`);
  }
  console.log(`   status     : ${green ? "\x1b[32mGREEN ✅\x1b[0m" : "\x1b[31mRED ❌\x1b[0m"}`);
  if (bus.dashboardUrl) console.log(`   dashboard  : ${bus.dashboardUrl}`);
  await bus.flush();
  if (!green) process.exit(1);
}

main().catch(async (err) => {
  console.error("\x1b[31mDemo failed:\x1b[0m", err);
  await bus.flush();
  process.exit(1);
});

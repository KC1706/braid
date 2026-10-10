import { fileURLToPath } from "node:url";
import { dirname, join, resolve } from "node:path";
import { readFileSync } from "node:fs";
import { seed } from "./seed.js";
import { integrate } from "../src/integrate.js";
import { UnionResolver, ClaudeResolver, HybridResolver } from "../src/resolver.js";

const __dirname = dirname(fileURLToPath(import.meta.url));
const repoRoot = resolve(__dirname, "../..");
const nodeModules = join(repoRoot, "node_modules");
const repoDir = resolve(__dirname, "../.tmp/demo-repo");

const TASK = "Add input validation and a rate limiter to the /users route.";

function banner(s: string) {
  console.log(`\n\x1b[1m\x1b[36m${s}\x1b[0m`);
}

async function main() {
  banner("① Seeding baseline + 3 agent forks (2 collide on getUser)…");
  const { baseline, agents } = await seed(repoDir);
  for (const a of agents) console.log(`   • ${a.id} [${a.branch}] — ${a.intent}`);

  const claude = new ClaudeResolver();
  const resolver = new HybridResolver(new UnionResolver(), claude);
  banner(
    `② Integrating with hybrid resolver (deterministic union → Claude on retry: ` +
      `${claude.available ? "LLM available" : "LLM offline, union only"})…`,
  );

  const { mergedSha, trail, green } = await integrate({
    repoDir,
    baseline,
    task: TASK,
    agents,
    resolver,
    verifyOptions: { nodeModules },
    log: (m) => console.log(m),
  });

  banner("③ Merged src/users.ts (both features survive):");
  console.log(readFileSync(join(repoDir, "src/users.ts"), "utf8").trimEnd());

  banner("④ .braid/trail.json — the WHY:");
  console.log(readFileSync(join(repoDir, ".braid/trail.json"), "utf8").trimEnd());

  banner("⑤ Result");
  console.log(`   merged sha : ${mergedSha.slice(0, 12)}`);
  console.log(`   agents     : ${trail.summary.agentsMerged} merged`);
  console.log(`   auto-merged: ${trail.summary.filesAutoMerged} file(s) (no AI)`);
  console.log(`   conflicts  : ${trail.summary.conflictsResolved} resolved ` +
    `(${trail.summary.llmResolutions} via LLM)`);
  console.log(`   final test : ${trail.finalTest}`);
  console.log(`   status     : ${green ? "\x1b[32mGREEN ✅\x1b[0m" : "\x1b[31mRED ❌\x1b[0m"}`);
  if (!green) process.exit(1);
}

main().catch((err) => {
  console.error("\x1b[31mDemo failed:\x1b[0m", err);
  process.exit(1);
});

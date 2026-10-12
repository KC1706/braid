import { describe, it, expect } from "vitest";
import { fileURLToPath } from "node:url";
import { dirname, resolve, join } from "node:path";
import { mkdirSync, writeFileSync, rmSync } from "node:fs";
import { gitOk } from "../src/git.js";
import { integrate, type AgentInput } from "../src/integrate.js";
import { UnionResolver } from "../src/resolver.js";
import { EscalatePolicy, PickWinnerPolicy } from "../src/policy.js";

const __dirname = dirname(fileURLToPath(import.meta.url));
const repoRoot = resolve(__dirname, "../..");
const nodeModules = join(repoRoot, "node_modules");

const PKG = JSON.stringify(
  { name: "braid-incompat-target", version: "0.0.0", private: true, type: "module", scripts: { test: "vitest run" } },
  null,
  2,
);
const VITEST = `import { defineConfig } from "vitest/config";\nexport default defineConfig({ test: { include: ["test/**/*.test.ts"] } });\n`;

// Frozen contract: price(10) must equal 10 (RATE == 1). This test file is an
// immutable merge input — any agent that breaks it is incompatible.
const PRICE_TEST = `import { describe, it, expect } from "vitest";\nimport { price } from "../src/price.js";\ndescribe("price", () => {\n  it("prices at the base rate", () => { expect(price(10)).toBe(10); });\n});\n`;
const PRICE_BASE = `export const RATE = 1;\nexport function price(n: number): number {\n  return n * RATE;\n}\n`;

async function writeAll(dir: string, files: Record<string, string>) {
  for (const [p, c] of Object.entries(files)) {
    const full = join(dir, p);
    mkdirSync(dirname(full), { recursive: true });
    writeFileSync(full, c);
  }
}

/** Baseline + agent-a (compatible, docs-only) + agent-b (breaks the frozen test). */
async function buildRepo(): Promise<{ repoDir: string; agents: AgentInput[] }> {
  const repoDir = resolve(__dirname, "../.tmp/incompat-repo");
  rmSync(repoDir, { recursive: true, force: true });
  mkdirSync(repoDir, { recursive: true });
  await gitOk(repoDir, ["init", "-q"]);
  await gitOk(repoDir, ["config", "user.email", "braid@example.com"]);
  await gitOk(repoDir, ["config", "user.name", "Braid"]);
  await gitOk(repoDir, ["checkout", "-b", "baseline"]);
  await writeAll(repoDir, {
    "package.json": PKG,
    "vitest.config.ts": VITEST,
    "src/price.ts": PRICE_BASE,
    "README.md": "# target\n",
    "test/price.test.ts": PRICE_TEST,
  });
  await gitOk(repoDir, ["add", "-A"]);
  await gitOk(repoDir, ["commit", "-q", "-m", "baseline"]);

  // agent-a: docs only (clean, keeps the suite green).
  await gitOk(repoDir, ["checkout", "-q", "-b", "agent/docs", "baseline"]);
  await writeAll(repoDir, { "README.md": "# target\n\nNow documented.\n" });
  await gitOk(repoDir, ["add", "-A"]);
  await gitOk(repoDir, ["commit", "-q", "-m", "agent-a: docs"]);

  // agent-b: changes the rate — price(10) becomes 20, breaking the frozen test.
  await gitOk(repoDir, ["checkout", "-q", "-b", "agent/rate", "baseline"]);
  await writeAll(repoDir, { "src/price.ts": PRICE_BASE.replace("RATE = 1", "RATE = 2") });
  await gitOk(repoDir, ["add", "-A"]);
  await gitOk(repoDir, ["commit", "-q", "-m", "agent-b: double the rate"]);

  await gitOk(repoDir, ["checkout", "-q", "baseline"]);
  return {
    repoDir,
    agents: [
      { id: "agent-a", branch: "agent/docs", intent: "documentation" },
      { id: "agent-b", branch: "agent/rate", intent: "double the rate" },
    ],
  };
}

describe("incompatibility path (C4) — union is not a law", () => {
  it("DEFAULT escalates: records both candidates, drops nothing, does not throw", async () => {
    const { repoDir, agents } = await buildRepo();
    const res = await integrate({
      repoDir,
      baseline: "baseline",
      task: "price work",
      agents,
      resolver: new UnionResolver(),
      verifyOptions: { nodeModules },
      maxRetries: 0,
      // default policy = EscalatePolicy
    });

    expect(res.verdict).toBe("incompatible");
    expect(res.green).toBe(false);
    expect(res.trail.incompatibilities).toHaveLength(1);
    const inc = res.trail.incompatibilities![0];
    expect(inc.agent).toBe("agent-b");
    expect(inc.action).toBe("escalate");
    expect(inc.dropped).toBeUndefined(); // nothing dropped
    // agent-a survived and stays integrated.
    expect(res.trail.agents.find((a) => a.agent === "agent-a")?.dropped).toBeFalsy();
  }, 120_000);

  it("pick-winner drops the loser and continues to green", async () => {
    const { repoDir, agents } = await buildRepo();
    const res = await integrate({
      repoDir,
      baseline: "baseline",
      task: "price work",
      agents,
      resolver: new UnionResolver(),
      verifyOptions: { nodeModules },
      maxRetries: 0,
      incompatibilityPolicy: new PickWinnerPolicy(["agent-a", "agent-b"]),
    });

    expect(res.verdict).toBe("green");
    expect(res.green).toBe(true);
    expect(res.trail.incompatibilities).toHaveLength(1);
    expect(res.trail.incompatibilities![0].action).toBe("drop");
    expect(res.trail.incompatibilities![0].dropped).toBe("agent-b");
    // The dropped agent is recorded, not silently lost.
    expect(res.trail.agents.find((a) => a.agent === "agent-b")?.dropped).toBe(true);
    expect(res.trail.summary.agentsMerged).toBe(1); // only agent-a counts
  }, 120_000);
});

import { describe, it, expect } from "vitest";
import { fileURLToPath } from "node:url";
import { dirname, resolve, join } from "node:path";
import { mkdirSync, writeFileSync, rmSync, readFileSync } from "node:fs";
import { gitOk } from "../src/git.js";
import { integrate, type AgentInput } from "../src/integrate.js";
import { UnionResolver } from "../src/resolver.js";

const __dirname = dirname(fileURLToPath(import.meta.url));
const repoRoot = resolve(__dirname, "../..");
const nodeModules = join(repoRoot, "node_modules");

const PKG = JSON.stringify(
  { name: "braid-semrepair-target", version: "0.0.0", private: true, type: "module", scripts: { test: "vitest run" } },
  null,
  2,
);
const VITEST = `import { defineConfig } from "vitest/config";\nexport default defineConfig({ test: { include: ["test/**/*.test.ts"] } });\n`;

async function writeAll(dir: string, files: Record<string, string>) {
  for (const [p, c] of Object.entries(files)) {
    const full = join(dir, p);
    mkdirSync(dirname(full), { recursive: true });
    writeFileSync(full, c);
  }
}

/**
 * agent-a renames getUser -> fetchUser (and fixes its own caller b.ts).
 * agent-b, branched from base, ADDS c.ts that still imports the old getUser.
 * The two touch disjoint files, so git merges them textually CLEAN — but c.ts's
 * import now resolves to nothing. Only Rung 3 catches it; Braid must auto-repair.
 */
async function buildRepo(): Promise<{ repoDir: string; agents: AgentInput[] }> {
  const repoDir = resolve(__dirname, "../.tmp/semrepair-repo");
  rmSync(repoDir, { recursive: true, force: true });
  mkdirSync(repoDir, { recursive: true });
  await gitOk(repoDir, ["init", "-q"]);
  await gitOk(repoDir, ["config", "user.email", "braid@example.com"]);
  await gitOk(repoDir, ["config", "user.name", "Braid"]);
  await gitOk(repoDir, ["checkout", "-b", "baseline"]);
  await writeAll(repoDir, {
    "package.json": PKG,
    "vitest.config.ts": VITEST,
    "src/a.ts": `export function getUser(id: string): string {\n  return "u:" + id;\n}\n`,
    "src/b.ts": `import { getUser } from "./a.js";\nexport function label(id: string): string {\n  return getUser(id);\n}\n`,
    "test/x.test.ts": `import { describe, it, expect } from "vitest";\nimport { label } from "../src/b.js";\ndescribe("label", () => { it("labels", () => { expect(label("1")).toBe("u:1"); }); });\n`,
  });
  await gitOk(repoDir, ["add", "-A"]);
  await gitOk(repoDir, ["commit", "-q", "-m", "baseline"]);

  // agent-a: rename getUser -> fetchUser, update its own caller.
  await gitOk(repoDir, ["checkout", "-q", "-b", "agent/rename", "baseline"]);
  await writeAll(repoDir, {
    "src/a.ts": `export function fetchUser(id: string): string {\n  return "u:" + id;\n}\n`,
    "src/b.ts": `import { fetchUser } from "./a.js";\nexport function label(id: string): string {\n  return fetchUser(id);\n}\n`,
  });
  await gitOk(repoDir, ["add", "-A"]);
  await gitOk(repoDir, ["commit", "-q", "-m", "agent-a: rename getUser->fetchUser"]);

  // agent-b: new file that still imports the OLD name.
  await gitOk(repoDir, ["checkout", "-q", "-b", "agent/consumer", "baseline"]);
  await writeAll(repoDir, {
    "src/c.ts": `import { getUser } from "./a.js";\nexport function helper(): string {\n  return getUser("x");\n}\n`,
  });
  await gitOk(repoDir, ["add", "-A"]);
  await gitOk(repoDir, ["commit", "-q", "-m", "agent-b: add consumer"]);

  await gitOk(repoDir, ["checkout", "-q", "baseline"]);
  return {
    repoDir,
    agents: [
      { id: "agent-a", branch: "agent/rename", intent: "rename getUser to fetchUser" },
      { id: "agent-b", branch: "agent/consumer", intent: "add a consumer of getUser" },
    ],
  };
}

describe("Rung 3 auto-fix (C1) — detect AND resolve a clean-but-broken merge", () => {
  it("repairs a dangling import left by a rename and goes green", async () => {
    const { repoDir, agents } = await buildRepo();
    const res = await integrate({
      repoDir,
      baseline: "baseline",
      task: "rename + consumer",
      agents,
      resolver: new UnionResolver(),
      verifyOptions: { nodeModules },
    });

    expect(res.verdict).toBe("green");
    expect(res.green).toBe(true);
    expect(res.trail.incompatibilities ?? []).toHaveLength(0);
    // The repair aliased the renamed export back to the name the importer used.
    const c = readFileSync(join(repoDir, "src/c.ts"), "utf8");
    expect(c).toContain("fetchUser as getUser");
    // Downstream references are untouched.
    expect(c).toContain('getUser("x")');
  }, 120_000);
});

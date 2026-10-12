import { describe, it, expect } from "vitest";
import { fileURLToPath } from "node:url";
import { dirname, join, resolve } from "node:path";
import { readFileSync } from "node:fs";
import { seed } from "../demo/seed.js";
import { integrate } from "../src/integrate.js";
import { UnionResolver } from "../src/resolver.js";

const __dirname = dirname(fileURLToPath(import.meta.url));
const repoRoot = resolve(__dirname, "../..");
const nodeModules = join(repoRoot, "node_modules");

// End-to-end proof: real git merge + deterministic resolver + real vitest gate.
describe("integrate (end-to-end)", () => {
  it("composes 3 agents into one green merge, both features surviving", async () => {
    const repoDir = resolve(__dirname, "../.tmp/test-repo");
    const { baseline, agents } = await seed(repoDir);

    const { trail, green } = await integrate({
      repoDir,
      baseline,
      task: "Add input validation and a rate limiter to the /users route.",
      agents,
      resolver: new UnionResolver(),
      verifyOptions: { nodeModules },
    });

    expect(green).toBe(true);
    expect(trail.summary.agentsMerged).toBe(3);
    expect(trail.summary.conflictsResolved).toBeGreaterThanOrEqual(1);
    expect(trail.finalTest).toMatch(/2 passed/);

    const merged = readFileSync(join(repoDir, "src/users.ts"), "utf8");
    expect(merged).toContain("validateId(id);");
    expect(merged).toContain("rateLimit(id);");
    expect(merged).toContain("return db.find(id);");
    expect(merged).not.toContain("<<<<<<<");
  }, 120_000);
});

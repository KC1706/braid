import { describe, it, expect } from "vitest";
import { fileURLToPath } from "node:url";
import { dirname, resolve, join } from "node:path";
import { writeFileSync, rmSync } from "node:fs";
import { seed } from "../demo/seed.js";
import { gitOk } from "../src/git.js";
import { countTestCases, isTestFile, structuralGuards } from "../src/ladder.js";

const __dirname = dirname(fileURLToPath(import.meta.url));

describe("countTestCases (static, no execution)", () => {
  it("counts it() and test() calls", () => {
    expect(countTestCases(`it("a", () => {}); test("b", () => {});`)).toBe(2);
  });
  it("does NOT count skipped tests — skipping reads as a coverage drop", () => {
    expect(countTestCases(`it.skip("a", () => {}); xit("b", () => {});`)).toBe(0);
  });
  it("ignores commented-out tests", () => {
    expect(countTestCases(`// it("a", () => {})\n/* test("b", () => {}) */\nit("c", () => {});`)).toBe(1);
  });
  it("does not count method calls like obj.it()", () => {
    expect(countTestCases(`foo.it(); bar.test();`)).toBe(0);
  });
});

describe("isTestFile", () => {
  it("matches common test paths", () => {
    expect(isTestFile("test/users.test.ts")).toBe(true);
    expect(isTestFile("src/__tests__/a.spec.tsx")).toBe(true);
    expect(isTestFile("src/users.ts")).toBe(false);
  });
});

describe("structural guards (Rung 1) — tests are immutable merge inputs", () => {
  async function freshRepo() {
    const repoDir = resolve(__dirname, "../.tmp/ladder-repo");
    rmSync(repoDir, { recursive: true, force: true });
    await seed(repoDir);
    // Put the baseline tree on disk as the "merged working tree" we inspect.
    await gitOk(repoDir, ["checkout", "-q", "baseline"]);
    return repoDir;
  }

  it("passes when the test suite is untouched", async () => {
    const repoDir = await freshRepo();
    const r = await structuralGuards(repoDir, "baseline");
    expect(r.passed).toBe(true);
  });

  it("REJECTS deleting a test file to go green (C2)", async () => {
    const repoDir = await freshRepo();
    rmSync(join(repoDir, "test/users.test.ts"));
    const r = await structuralGuards(repoDir, "baseline");
    expect(r.passed).toBe(false);
    expect(r.detail).toMatch(/test file deleted/);
  });

  it("REJECTS weakening a test file (removing / skipping assertions) (C2)", async () => {
    const repoDir = await freshRepo();
    // Rewrite the 2-case suite down to a single trivial test.
    writeFileSync(
      join(repoDir, "test/users.test.ts"),
      `import { describe, it, expect } from "vitest";\ndescribe("x", () => { it("noop", () => { expect(true).toBe(true); }); });\n`,
    );
    const r = await structuralGuards(repoDir, "baseline");
    expect(r.passed).toBe(false);
    expect(r.detail).toMatch(/coverage dropped|test cases dropped/);
  });

  it("allows ADDING tests (coverage going up is fine)", async () => {
    const repoDir = await freshRepo();
    writeFileSync(
      join(repoDir, "test/extra.test.ts"),
      `import { it, expect } from "vitest";\nit("extra", () => { expect(1).toBe(1); });\n`,
    );
    const r = await structuralGuards(repoDir, "baseline");
    expect(r.passed).toBe(true);
  });
});

import { readFileSync, existsSync } from "node:fs";
import { join } from "node:path";
import { lsFiles, showFile } from "./git.js";
import { verify, type VerifyOptions, type VerifyResult } from "./verify.js";

/**
 * The verification ladder (PRODUCTION_PLAN.md §5.1).
 *
 * Replaces "run the suite, check the exit code" with a deterministic,
 * cheapest-first ladder of rungs. Tests are *necessary, not sufficient*, and —
 * per Decision 2 — they are IMMUTABLE merge inputs: the merge engine is
 * structurally forbidden from editing them or reducing coverage to go green.
 *
 * A single exit code is never the gate. Each rung can fail the merge on its own,
 * and the cheap deterministic rungs run before anything is executed, so the
 * common "delete-the-test to pass" attack is caught before a test ever runs.
 *
 * Rungs implemented so far:
 *   - Rung 1  structural guards (deterministic, pre-execution)  — this file
 *   - Rung 4  test execution (the existing real-suite gate)     — verify.ts
 * Rungs 0/2/3/5 and the incompatibility path land in later chunks.
 */

/** A verdict is more than a boolean — see §5.1 / §4 C2–C4. */
export type Verdict =
  | "green" // every rung that ran passed
  | "red" // a rung failed (tests red, or a guard rejected the merge)
  | "inconclusive" // could not decide (e.g. flaky / infra) — never treated as green
  | "incompatible"; // the candidates cannot be composed (reserved for the §5.1 path)

export interface RungResult {
  rung: number;
  name: string;
  passed: boolean;
  /** Short human-readable line for the trail / dashboard. */
  summary: string;
  /** Detail for a failure (fed back into a resolver retry). */
  detail?: string;
}

export interface LadderResult {
  verdict: Verdict;
  /** Convenience: verdict === "green". Keeps call sites that only need a bool simple. */
  passed: boolean;
  /** One-line summary of the deciding rung (for logs / events / trail). */
  summary: string;
  /** Failure detail of the deciding rung, for resolver retry feedback. */
  output: string;
  rungs: RungResult[];
}

export interface LadderOptions extends VerifyOptions {
  /**
   * Globs (substring/suffix match) identifying test files. A file matching any
   * of these is a frozen merge input: it may not be deleted and its executable
   * test-case count may not drop relative to the baseline.
   */
  testFilePatterns?: RegExp[];
}

export const DEFAULT_TEST_PATTERNS: RegExp[] = [
  /\.(test|spec)\.[cm]?[jt]sx?$/,
  /(^|\/)(test|tests|__tests__)\//,
];

export function isTestFile(path: string, patterns = DEFAULT_TEST_PATTERNS): boolean {
  return patterns.some((re) => re.test(path));
}

/**
 * Count executable test cases in a source string. Deterministic and static (no
 * execution). `it(` / `test(` count; crucially `it.skip(` / `test.skip(` /
 * `xit(` / `describe.skip(` do NOT match, so skipping a test reads as a
 * coverage regression — which is the whole point of the guard.
 */
export function countTestCases(src: string): number {
  // Strip line + block comments so commented-out tests don't inflate the count.
  const stripped = src
    .replace(/\/\*[\s\S]*?\*\//g, "")
    .replace(/(^|[^:])\/\/.*$/gm, "$1");
  const m = stripped.match(/(?<![.\w])(?:it|test)\s*(?:\.\s*each\s*\([^)]*\)\s*)?\(/g);
  return m ? m.length : 0;
}

/**
 * Rung 1 — structural guards. Compares the frozen baseline tree against the
 * merged working tree and rejects the merge if any test file was removed or had
 * its executable test-case count reduced (per-file or in total). This is the
 * mechanical enforcement of Decision 2: fixes C2 ("the cheapest way to green is
 * to delete the test") and is the first line against C14 (injection that edits
 * tests). No tests are executed here.
 */
export async function structuralGuards(
  repoDir: string,
  baselineRef: string,
  patterns = DEFAULT_TEST_PATTERNS,
): Promise<RungResult> {
  const baseFiles = (await lsFiles(repoDir, baselineRef)).filter((f) => isTestFile(f, patterns));

  const violations: string[] = [];
  let baseTotal = 0;
  let nowTotal = 0;

  for (const file of baseFiles) {
    const baseSrc = (await showFile(repoDir, baselineRef, file)) ?? "";
    const baseCount = countTestCases(baseSrc);
    baseTotal += baseCount;

    const abs = join(repoDir, file);
    if (!existsSync(abs)) {
      violations.push(`test file deleted: ${file} (had ${baseCount} test case(s))`);
      continue;
    }
    const nowSrc = readFileSync(abs, "utf8");
    const nowCount = countTestCases(nowSrc);
    nowTotal += nowCount;
    if (nowCount < baseCount) {
      violations.push(
        `test coverage dropped in ${file}: ${baseCount} → ${nowCount} executable test case(s)`,
      );
    }
  }

  if (nowTotal < baseTotal) {
    violations.push(`total executable test cases dropped: ${baseTotal} → ${nowTotal}`);
  }

  if (violations.length > 0) {
    return {
      rung: 1,
      name: "structural-guards",
      passed: false,
      summary: `structural guard rejected the merge (${violations.length} violation(s))`,
      detail:
        "Test files are immutable merge inputs; the merge may not remove or weaken them.\n" +
        violations.map((v) => `  - ${v}`).join("\n"),
    };
  }

  return {
    rung: 1,
    name: "structural-guards",
    passed: true,
    summary: `structural guards ok (${baseFiles.length} test file(s), ${baseTotal} test case(s) preserved)`,
  };
}

/**
 * Run the ladder against the current merged working tree. Cheapest-first: the
 * deterministic structural guards (Rung 1) gate BEFORE the real suite executes
 * (Rung 4), so a merge that weakened the tests is rejected without ever getting
 * the chance to "pass".
 */
export async function runLadder(
  repoDir: string,
  baselineRef: string,
  opts: LadderOptions,
): Promise<LadderResult> {
  const patterns = opts.testFilePatterns ?? DEFAULT_TEST_PATTERNS;
  const rungs: RungResult[] = [];

  const guard = await structuralGuards(repoDir, baselineRef, patterns);
  rungs.push(guard);
  if (!guard.passed) {
    return {
      verdict: "red",
      passed: false,
      summary: guard.summary,
      output: guard.detail ?? guard.summary,
      rungs,
    };
  }

  const v: VerifyResult = await verify(repoDir, opts);
  rungs.push({
    rung: 4,
    name: "test-execution",
    passed: v.passed,
    summary: v.summary,
    detail: v.passed ? undefined : v.output,
  });

  return {
    verdict: v.passed ? "green" : "red",
    passed: v.passed,
    summary: v.summary,
    output: v.output,
    rungs,
  };
}

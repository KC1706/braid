import { execFile } from "node:child_process";
import { promisify } from "node:util";
import { existsSync, symlinkSync } from "node:fs";
import { join } from "node:path";

const pexec = promisify(execFile);

export interface VerifyResult {
  passed: boolean;
  summary: string;
  /** Tail of combined output, for feeding a failure back into a resolver retry. */
  output: string;
}

export interface VerifyOptions {
  /**
   * Absolute path to a node_modules dir to symlink into the merged tree so the
   * test runner resolves its deps. In production this is the Sandbox's install;
   * locally it's the repo-root node_modules.
   */
  nodeModules: string;
  /** Command + args to run the tests. Defaults to vitest. */
  command?: string[];
  timeoutMs?: number;
}

/**
 * THE acceptance gate. Runs the project's actual test suite inside the merged
 * working tree and reports deterministic pass/fail. No model opinion decides
 * correctness here — only exit code 0 counts as green.
 */
export async function verify(cwd: string, opts: VerifyOptions): Promise<VerifyResult> {
  const link = join(cwd, "node_modules");
  if (!existsSync(link)) {
    try {
      symlinkSync(opts.nodeModules, link, "dir");
    } catch {
      /* best effort; a real Sandbox would `npm install` instead */
    }
  }

  const [cmd, ...args] = opts.command ?? [
    join(opts.nodeModules, ".bin", "vitest"),
    "run",
    "--no-color",
  ];

  try {
    const { stdout, stderr } = await pexec(cmd, args, {
      cwd,
      timeout: opts.timeoutMs ?? 120_000,
      maxBuffer: 32 * 1024 * 1024,
      env: { ...process.env, CI: "true" },
    });
    const output = (stdout + stderr).trim();
    return { passed: true, summary: summarize(output), output: tail(output) };
  } catch (err) {
    const e = err as { stdout?: string; stderr?: string };
    const output = ((e.stdout ?? "") + (e.stderr ?? "")).trim();
    return { passed: false, summary: summarize(output), output: tail(output) };
  }
}

function summarize(output: string): string {
  const m = output.match(/Tests\s+.*?(\d+)\s+passed.*?(?:\((\d+)\))?/i);
  const line = output.split("\n").find((l) => /Tests\s+/.test(l));
  return line?.trim() ?? (m ? m[0] : "no test summary found");
}

function tail(output: string, lines = 40): string {
  return output.split("\n").slice(-lines).join("\n");
}

import { execFile } from "node:child_process";
import { promisify } from "node:util";

const pexec = promisify(execFile);

export interface GitResult {
  stdout: string;
  stderr: string;
  code: number;
}

/**
 * Run a git command in `cwd`. Never throws on a non-zero exit — git uses exit
 * codes as signal (e.g. `git merge` exits 1 on conflicts), and the merge engine
 * needs to inspect those codes rather than treat them as crashes.
 */
export async function git(cwd: string, args: string[]): Promise<GitResult> {
  try {
    const { stdout, stderr } = await pexec("git", args, {
      cwd,
      maxBuffer: 64 * 1024 * 1024,
    });
    return { stdout, stderr, code: 0 };
  } catch (err) {
    const e = err as { stdout?: string; stderr?: string; code?: number };
    return { stdout: e.stdout ?? "", stderr: e.stderr ?? "", code: e.code ?? 1 };
  }
}

/** Assert a git command succeeded; throw with context otherwise. */
export async function gitOk(cwd: string, args: string[]): Promise<string> {
  const r = await git(cwd, args);
  if (r.code !== 0) {
    throw new Error(`git ${args.join(" ")} failed (code ${r.code}):\n${r.stderr || r.stdout}`);
  }
  return r.stdout;
}

export async function currentSha(cwd: string): Promise<string> {
  return (await gitOk(cwd, ["rev-parse", "HEAD"])).trim();
}

/** Files left in a conflicted (unmerged) state after a merge attempt. */
export async function conflictedFiles(cwd: string): Promise<string[]> {
  const out = await gitOk(cwd, ["diff", "--name-only", "--diff-filter=U"]);
  return out.split("\n").map((s) => s.trim()).filter(Boolean);
}

/** List of files that differ between two refs (for provenance / "what changed"). */
export async function changedFiles(cwd: string, from: string, to: string): Promise<string[]> {
  const out = await gitOk(cwd, ["diff", "--name-only", from, to]);
  return out.split("\n").map((s) => s.trim()).filter(Boolean);
}

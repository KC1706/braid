import { readFileSync, writeFileSync, mkdirSync } from "node:fs";
import { join } from "node:path";
import { git, gitOk, currentSha, conflictedFiles, changedFiles } from "./git.js";
import { parseConflicts, applyResolutions } from "./conflict.js";
import type { Resolver } from "./resolver.js";
import { verify, type VerifyOptions } from "./verify.js";
import type { Trail, ConflictRecord, AgentMergeRecord } from "./trail.js";
import type { BraidEvent } from "./events.js";

export interface AgentInput {
  id: string;
  branch: string;
  intent: string;
}

export interface IntegrateOptions {
  repoDir: string;
  baseline: string; // base branch/ref, e.g. "baseline" or "main"
  integrationBranch?: string; // defaults to "braid/integration"
  task: string;
  agents: AgentInput[];
  resolver: Resolver;
  verifyOptions: VerifyOptions;
  maxRetries?: number;
  log?: (msg: string) => void;
  /** Optional structured-event sink (streams the live dashboard). */
  onEvent?: (event: BraidEvent) => void;
}

export interface IntegrateResult {
  mergedSha: string;
  trail: Trail;
  green: boolean;
}

/**
 * The hybrid merge engine. Iteratively `git merge`s each agent branch into one
 * integration branch:
 *   - non-overlapping changes  -> git auto-resolves (deterministic, free)
 *   - a true conflict hunk     -> handed to the resolver with full context
 *   - after every merge        -> real test suite decides pass/fail
 *   - red                      -> re-resolve with the failure fed back (bounded)
 * Finally writes .braid/trail.json and returns the merged sha.
 */
export async function integrate(opts: IntegrateOptions): Promise<IntegrateResult> {
  const {
    repoDir,
    baseline,
    task,
    agents,
    resolver,
    verifyOptions,
    maxRetries = 2,
  } = opts;
  const integrationBranch = opts.integrationBranch ?? "braid/integration";
  const log = opts.log ?? (() => {});
  const emit = opts.onEvent ?? (() => {});

  await gitOk(repoDir, ["config", "merge.conflictStyle", "diff3"]);
  await gitOk(repoDir, ["checkout", "-B", integrationBranch, baseline]);
  log(`starting integration on ${integrationBranch} from ${baseline}`);

  const conflicts: ConflictRecord[] = [];
  const agentRecords: AgentMergeRecord[] = [];
  const integratedIntents: string[] = [];

  for (const agent of agents) {
    const parent = await currentSha(repoDir);
    log(`\n▶ merging ${agent.id} (${agent.branch}) — intent: "${agent.intent}"`);
    emit({ type: "merge:start", agent: agent.id });

    const merge = await git(repoDir, [
      "merge",
      "--no-edit",
      "--no-ff",
      agent.branch,
    ]);
    const conflicted = merge.code === 0 ? [] : await conflictedFiles(repoDir);

    const record: AgentMergeRecord = {
      agent: agent.id,
      branch: agent.branch,
      intent: agent.intent,
      autoMergedFiles: [],
      conflictedFiles: conflicted,
      testResult: "",
    };

    if (conflicted.length === 0) {
      // Clean, deterministic auto-merge — git already created the commit.
      record.autoMergedFiles = await changedFiles(repoDir, parent, "HEAD");
      log(`  ✓ git auto-merged ${record.autoMergedFiles.length} file(s) — no AI needed`);
      emit({ type: "merge:auto", agent: agent.id, files: record.autoMergedFiles });
    } else {
      log(`  ⚡ ${conflicted.length} file(s) conflicted — resolving true overlaps`);
      emit({ type: "conflict:detected", agent: agent.id, files: conflicted });
      const oursIntent = integratedIntents.join(" + ") || baseline;

      for (const file of conflicted) {
        await resolveFileWithRetries({
          repoDir,
          file,
          oursIntent,
          agent,
          resolver,
          verifyOptions,
          maxRetries,
          conflicts,
          log,
          emit,
        });
      }

      // All conflicted files resolved and staged — commit the merge.
      await gitOk(repoDir, [
        "commit",
        "--no-edit",
        "-m",
        `braid: merge ${agent.id} (${agent.intent})`,
      ]);
      record.autoMergedFiles = (await changedFiles(repoDir, parent, "HEAD")).filter(
        (f) => !conflicted.includes(f),
      );
    }

    // Verify the merged state regardless of path.
    const v = await verify(repoDir, verifyOptions);
    record.testResult = v.summary;
    log(`  ${v.passed ? "✅" : "❌"} ${v.summary}`);
    emit({ type: "verify", scope: agent.id, passed: v.passed, summary: v.summary });
    if (!v.passed) {
      // A clean auto-merge that still breaks tests: surface it honestly.
      throw new Error(
        `Merging ${agent.id} broke the suite and could not be reconciled:\n${v.output}`,
      );
    }

    integratedIntents.push(agent.intent);
    agentRecords.push(record);
  }

  const mergedSha = await currentSha(repoDir);
  const finalVerify = await verify(repoDir, verifyOptions);
  emit({ type: "verify", scope: "final", passed: finalVerify.passed, summary: finalVerify.summary });

  const trail: Trail = {
    task,
    createdAt: new Date().toISOString(),
    baseline,
    mergedSha,
    agents: agentRecords,
    conflicts,
    finalTest: finalVerify.summary,
    summary: {
      agentsMerged: agentRecords.length,
      filesAutoMerged: new Set(agentRecords.flatMap((a) => a.autoMergedFiles)).size,
      conflictsResolved: conflicts.length,
      llmResolutions: conflicts.filter((c) => c.strategy === "llm").length,
    },
  };

  const trailDir = join(repoDir, ".braid");
  mkdirSync(trailDir, { recursive: true });
  writeFileSync(join(trailDir, "trail.json"), JSON.stringify(trail, null, 2) + "\n");
  await gitOk(repoDir, ["add", ".braid/trail.json"]);
  await gitOk(repoDir, ["commit", "-m", "braid: provenance trail"]);

  return { mergedSha: await currentSha(repoDir), trail, green: finalVerify.passed };
}

interface ResolveFileArgs {
  repoDir: string;
  file: string;
  oursIntent: string;
  agent: AgentInput;
  resolver: Resolver;
  verifyOptions: VerifyOptions;
  maxRetries: number;
  conflicts: ConflictRecord[];
  log: (msg: string) => void;
  emit: (event: BraidEvent) => void;
}

/**
 * Resolve every conflict hunk in one file, then prove it with the test suite.
 * On red, re-resolve with attempt+1 and the failure text fed back, up to
 * maxRetries. The resolver (hybrid) escalates to the LLM on retry.
 */
async function resolveFileWithRetries(a: ResolveFileArgs): Promise<void> {
  const { repoDir, file, oursIntent, agent, resolver, verifyOptions, maxRetries, conflicts, log, emit } = a;
  const abs = join(repoDir, file);
  const conflictedContent = readFileSync(abs, "utf8");
  const hunks = parseConflicts(conflictedContent);
  let lastFailure: string | undefined;

  for (let attempt = 0; attempt <= maxRetries; attempt++) {
    const outputs = [];
    for (const hunk of hunks) {
      outputs.push(
        await resolver.resolve({
          path: file,
          hunk,
          oursIntent,
          theirsIntent: agent.intent,
          attempt,
          lastFailure,
        }),
      );
    }
    emit({ type: "conflict:attempt", agent: agent.id, path: file, attempt: attempt + 1, strategy: outputs[0]?.strategy ?? "?" });
    const resolved = applyResolutions(conflictedContent, outputs.map((o) => o.content));
    writeFileSync(abs, resolved);
    await gitOk(repoDir, ["add", file]);

    const v = await verify(repoDir, verifyOptions);
    if (v.passed) {
      hunks.forEach((hunk, i) => {
        conflicts.push({
          path: file,
          base: hunk.base.join("\n"),
          candidates: [
            { agent: "integrated", intent: oursIntent, content: hunk.ours.join("\n") },
            { agent: agent.id, intent: agent.intent, content: hunk.theirs.join("\n") },
          ],
          resolution: outputs[i].content,
          rationale: outputs[i].rationale,
          strategy: outputs[i].strategy,
          verified: v.summary,
          attempts: attempt + 1,
        });
      });
      log(
        `    ✓ ${file}: ${hunks.length} hunk(s) via ${outputs[0]?.strategy ?? "?"} ` +
          `(attempt ${attempt + 1}) — ${v.summary}`,
      );
      emit({
        type: "conflict:resolved",
        agent: agent.id,
        path: file,
        strategy: outputs[0]?.strategy ?? "?",
        attempts: attempt + 1,
        verified: v.summary,
      });
      return;
    }
    lastFailure = v.output;
    log(`    ↻ ${file}: attempt ${attempt + 1} red, retrying with failure fed back`);
  }

  throw new Error(`Could not reconcile ${file} after ${maxRetries + 1} attempts:\n${lastFailure}`);
}

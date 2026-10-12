import { readFileSync, writeFileSync, mkdirSync } from "node:fs";
import { join } from "node:path";
import { git, gitOk, currentSha, conflictedFiles, changedFiles, showFile } from "./git.js";
import { parseConflicts, applyResolutions } from "./conflict.js";
import type { Resolver } from "./resolver.js";
import { runLadder, type LadderOptions, type LadderResult, type Verdict } from "./ladder.js";
import { semanticConflicts, inferRename, aliasImport } from "./semantic.js";
import type { Trail, ConflictRecord, AgentMergeRecord, IncompatibilityRecord } from "./trail.js";
import type { BraidEvent } from "./events.js";
import { EscalatePolicy, type IncompatibilityPolicy } from "./policy.js";

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
  verifyOptions: LadderOptions;
  maxRetries?: number;
  /**
   * What to do when an agent cannot be composed with the integrated set.
   * Defaults to EscalatePolicy (stop + surface, never silently drop).
   */
  incompatibilityPolicy?: IncompatibilityPolicy;
  log?: (msg: string) => void;
  /** Optional structured-event sink (streams the live dashboard). */
  onEvent?: (event: BraidEvent) => void;
}

export interface IntegrateResult {
  mergedSha: string;
  trail: Trail;
  green: boolean;
  /** Overall run verdict. "incompatible" means an agent could not be composed. */
  verdict: Verdict;
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
  const policy = opts.incompatibilityPolicy ?? new EscalatePolicy();
  const log = opts.log ?? (() => {});
  const emit = opts.onEvent ?? (() => {});

  await gitOk(repoDir, ["config", "merge.conflictStyle", "diff3"]);
  await gitOk(repoDir, ["checkout", "-B", integrationBranch, baseline]);
  log(`starting integration on ${integrationBranch} from ${baseline}`);

  const conflicts: ConflictRecord[] = [];
  const agentRecords: AgentMergeRecord[] = [];
  const incompatibilities: IncompatibilityRecord[] = [];
  const integratedIntents: string[] = [];
  const integratedAgents: AgentInput[] = [];
  let runVerdict: Verdict = "green";

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

    // Reason this agent could not be composed, if any (drives the policy below).
    let incompatible: { reason: string; file?: string; lastFailure?: string } | null = null;
    // Conflict provenance for this agent — only committed to the trail if the
    // whole agent merge succeeds, so an aborted merge leaves no partial records.
    const agentConflicts: ConflictRecord[] = [];

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
        const r = await resolveFileWithRetries({
          repoDir,
          baseline,
          file,
          oursIntent,
          agent,
          resolver,
          verifyOptions,
          maxRetries,
          conflicts: agentConflicts,
          log,
          emit,
        });
        if (!r.resolved) {
          incompatible = {
            reason: `conflict in ${file} could not be reconciled after ${maxRetries + 1} attempt(s)`,
            file,
            lastFailure: r.lastFailure,
          };
          break;
        }
      }

      if (!incompatible) {
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
    }

    // Verify the merged state through the full ladder (structural guards +
    // semantic graph + real tests), when the merge itself succeeded. A clean
    // merge that fails ONLY the semantic rung is first routed through
    // deterministic rename-repair (Rung 3 auto-fix) before being judged.
    if (!incompatible) {
      const v = await verifyWithSemanticRepair({
        repoDir,
        baseline,
        verifyOptions,
        agent,
        log,
        emit,
      });
      record.testResult = v.summary;
      log(`  ${v.passed ? "✅" : "❌"} [${v.verdict}] ${v.summary}`);
      emit({
        type: "verify",
        scope: agent.id,
        passed: v.passed,
        summary: v.summary,
        verdict: v.verdict,
        rungs: v.rungs.map((r) => ({ rung: r.rung, name: r.name, passed: r.passed, summary: r.summary })),
      });
      if (!v.passed) {
        // A clean auto-merge that breaks verification (incl. a semantic conflict
        // or a tests-weakened guard) that no resolution could fix: incompatible.
        incompatible = { reason: `verification failed [${v.verdict}] and could not be reconciled`, lastFailure: v.output };
      }
    }

    if (incompatible) {
      // Restore the integration branch to the last good state — the merge is
      // undone, not left half-applied.
      await abortOrReset(repoDir, parent);

      const decision = policy.decide({
        agent,
        integratedAgents,
        file: incompatible.file,
        reason: incompatible.reason,
        lastFailure: incompatible.lastFailure,
      });
      const incRecord: IncompatibilityRecord = {
        agent: agent.id,
        intent: agent.intent,
        withIntents: integratedIntents.slice(),
        reason: incompatible.reason,
        file: incompatible.file,
        detail: incompatible.lastFailure ? tail(incompatible.lastFailure) : undefined,
        action: decision.action,
        dropped: decision.action === "drop" ? decision.drop : undefined,
        note: decision.note,
      };
      incompatibilities.push(incRecord);
      emit({
        type: "incompatible",
        agent: agent.id,
        reason: incompatible.reason,
        action: decision.action,
        dropped: incRecord.dropped,
        note: decision.note,
      });

      if (decision.action === "escalate") {
        log(`  ⛔ incompatible: ${decision.note}`);
        runVerdict = "incompatible";
        break; // stop the run; surface for a human/arbiter. Nothing is dropped.
      }

      // "drop": discard this agent's work (recorded), keep the integrated set.
      log(`  ⚠ dropped ${agent.id}: ${decision.note}`);
      record.dropped = true;
      record.dropReason = decision.note;
      record.testResult = `dropped (incompatible): ${incompatible.reason}`;
      agentRecords.push(record);
      continue;
    }

    // Agent composed cleanly — commit its conflict provenance and advance.
    conflicts.push(...agentConflicts);
    integratedIntents.push(agent.intent);
    integratedAgents.push(agent);
    agentRecords.push(record);
  }

  const mergedSha = await currentSha(repoDir);
  const finalVerify = await runLadder(repoDir, baseline, verifyOptions);
  const finalRungs = finalVerify.rungs.map((r) => ({
    rung: r.rung,
    name: r.name,
    passed: r.passed,
    summary: r.summary,
  }));
  emit({
    type: "verify",
    scope: "final",
    passed: finalVerify.passed,
    summary: finalVerify.summary,
    verdict: finalVerify.verdict,
    rungs: finalRungs,
  });

  // The run is green only if nothing escalated AND the final tree verifies.
  if (runVerdict !== "incompatible") runVerdict = finalVerify.verdict;
  const green = runVerdict === "green" && finalVerify.passed;

  const trail: Trail = {
    task,
    createdAt: new Date().toISOString(),
    baseline,
    mergedSha,
    agents: agentRecords,
    conflicts,
    incompatibilities: incompatibilities.length ? incompatibilities : undefined,
    verdict: runVerdict,
    finalTest: finalVerify.summary,
    finalVerification: {
      verdict: finalVerify.verdict,
      summary: finalVerify.summary,
      rungs: finalRungs,
    },
    summary: {
      agentsMerged: agentRecords.filter((a) => !a.dropped).length,
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

  return { mergedSha: await currentSha(repoDir), trail, green, verdict: runVerdict };
}

/** Undo an in-progress or just-committed merge, restoring HEAD to `parent`. */
async function abortOrReset(repoDir: string, parent: string): Promise<void> {
  const aborted = await git(repoDir, ["merge", "--abort"]);
  if (aborted.code !== 0) {
    // No merge in progress (clean auto-merge already committed) — hard reset.
    await gitOk(repoDir, ["reset", "--hard", parent]);
  }
}

function tail(text: string, lines = 30): string {
  return text.split("\n").slice(-lines).join("\n");
}

interface SemanticRepairArgs {
  repoDir: string;
  baseline: string;
  verifyOptions: LadderOptions;
  agent: AgentInput;
  log: (msg: string) => void;
  emit: (event: BraidEvent) => void;
}

/** True when the ladder's only failing rung is the semantic-conflict graph. */
function isSemanticOnlyFailure(v: LadderResult): boolean {
  const failed = v.rungs.filter((r) => !r.passed);
  return failed.length === 1 && failed[0].name === "semantic-conflicts";
}

/**
 * Run the ladder; if a textually-clean merge fails ONLY because of dangling
 * cross-module references (Rung 3), route each one through deterministic
 * rename-repair (alias the renamed export back to the name the importer uses),
 * commit the repair, and re-verify. Bounded. This turns Rung 3 from "detect and
 * fail" into "detect and resolve" — the §5.1 intent, fully deterministic and
 * with the real tests still the final gate (C1).
 */
async function verifyWithSemanticRepair(a: SemanticRepairArgs): Promise<LadderResult> {
  const { repoDir, baseline, verifyOptions, agent, log, emit } = a;
  let v = await runLadder(repoDir, baseline, verifyOptions);

  let round = 0;
  const maxRounds = 6; // bounded; each round fixes at least one reference or stops
  while (!v.passed && isSemanticOnlyFailure(v) && round < maxRounds) {
    const repaired = await repairOneSemanticConflict(repoDir, baseline, log);
    if (!repaired) break; // nothing we can safely infer — hand to the policy
    emit({ type: "log", line: `semantic repair: ${repaired}` });
    await gitOk(repoDir, ["add", "-A"]);
    await gitOk(repoDir, ["commit", "--no-edit", "-m", `braid: semantic repair for ${agent.id} (${repaired})`]);
    v = await runLadder(repoDir, baseline, verifyOptions);
    round++;
  }
  return v;
}

/**
 * Fix a single dangling import by rename-aliasing, if one is unambiguously
 * inferable. Returns a short description of what it repaired, or null if nothing
 * safe could be done. One per call so AST edit offsets never collide; the caller
 * loops and re-detects.
 */
async function repairOneSemanticConflict(repoDir: string, baseline: string, log: (m: string) => void): Promise<string | null> {
  const { conflicts } = semanticConflicts(repoDir);
  for (const c of conflicts) {
    if (c.kind !== "missing-export") continue;
    const baseSrc = await showFile(repoDir, baseline, c.targetFile);
    const nowSrc = readFileSync(join(repoDir, c.targetFile), "utf8");
    const newName = inferRename(baseSrc, nowSrc, c.targetFile, c.name);
    if (!newName) continue;
    const ok = aliasImport(join(repoDir, c.fromFile), c.moduleSpecifier, c.name, newName);
    if (ok) {
      const msg = `${c.fromFile}: import { ${c.name} } → { ${newName} as ${c.name} } from "${c.moduleSpecifier}"`;
      log(`    🔧 ${msg} (renamed in ${c.targetFile})`);
      return msg;
    }
  }
  return null;
}

interface ResolveFileArgs {
  repoDir: string;
  baseline: string;
  file: string;
  oursIntent: string;
  agent: AgentInput;
  resolver: Resolver;
  verifyOptions: LadderOptions;
  maxRetries: number;
  conflicts: ConflictRecord[];
  log: (msg: string) => void;
  emit: (event: BraidEvent) => void;
}

type ResolveFileResult = { resolved: true } | { resolved: false; lastFailure: string };

/**
 * Resolve every conflict hunk in one file, then prove it with the ladder.
 * On red, re-resolve with attempt+1 and the failure text fed back, up to
 * maxRetries. The resolver (hybrid) escalates to the LLM on retry. If every
 * attempt stays red, returns `{ resolved: false }` so the caller can apply the
 * incompatibility policy — it no longer throws (C4).
 */
async function resolveFileWithRetries(a: ResolveFileArgs): Promise<ResolveFileResult> {
  const { repoDir, baseline, file, oursIntent, agent, resolver, verifyOptions, maxRetries, conflicts, log, emit } = a;
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

    const v = await runLadder(repoDir, baseline, verifyOptions);
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
      return { resolved: true };
    }
    lastFailure = v.output;
    log(`    ↻ ${file}: attempt ${attempt + 1} red, retrying with failure fed back`);
  }

  log(`    ⛔ ${file}: unresolved after ${maxRetries + 1} attempt(s) — incompatible`);
  return { resolved: false, lastFailure: lastFailure ?? "no failure output captured" };
}

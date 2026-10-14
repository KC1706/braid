/**
 * The integration orchestration (GO_PRODUCTION_PLAN.md §3 Phase 3c), as a pure
 * function over injected zone clients + an event sink, so it unit-tests with
 * fakes and no Cloudflare runtime. workflow.ts wraps each call in a durable
 * Workflow step; index.ts enqueues a job by starting that Workflow.
 *
 * The flow is the two-zone hand-off: the io zone snapshots the base + forks into
 * a content-addressed bundle, the compute zone merges + runs the tests on that
 * bundle (no token, no egress), and — only on green — the io zone CAS-publishes
 * the merged bundle back to the base. Tokens are handed only to io-zone calls.
 */

import {
  BaseMovedError,
  type AgentRefJSON,
  type ComputeZoneClient,
  type IOZoneClient,
  type RepoRef,
} from "./ztypes";

/** IntegrationJob is the enqueued unit of work (the Workflow's params). */
export interface IntegrationJob {
  runId: string;
  /** Stable base identity (the base repo name) — the Base-Coordinator DO key. */
  baseKey: string;
  task?: string;
  resolver?: string;
  tree?: boolean;
  base: RepoRef;
  agents: AgentRefJSON[]; // each with remote/branch/token for the io snapshot
  eventSecret: string;
}

/** A structured event for the run's Orchestrator DO (signed by the emitter). */
export interface RunEvent {
  type: string;
  [k: string]: unknown;
}

export interface IntegrationDeps {
  io: IOZoneClient;
  compute: ComputeZoneClient;
  /** emit signs + posts an event to the run's Orchestrator DO. */
  emit: (event: RunEvent) => Promise<void>;
  /** recordPush notes a published SHA so a later self-push event is dropped (C9). */
  recordPush?: (sha: string) => Promise<void>;
  /** record appends a signed provenance entry to the ledger (Phase 4b). Optional:
   *  if the signing key is unset the ledger is simply not written. */
  record?: (kind: string, payload: Record<string, string>) => Promise<void>;
  now: () => string;
}

export type IntegrationStatus = "succeeded" | "failed" | "incompatible" | "base_moved";

export interface IntegrationOutcome {
  status: IntegrationStatus;
  verdict: string;
  mergedSha?: string;
  pushedSha?: string;
}

/**
 * runIntegration executes one run end-to-end over the injected zones. It never
 * hands a token to the compute zone: the IntegrateRequest it builds carries only
 * the bundle + agent id/intent. A base-moved publish surfaces as status
 * "base_moved" (the caller/Workflow may rebase + retry; Phase 4's coordinator).
 */
export async function runIntegration(deps: IntegrationDeps, job: IntegrationJob): Promise<IntegrationOutcome> {
  // run:init is the first event, so the projection is fully rebuildable from the
  // log alone (task + the agent roster live in the stream, not a side channel).
  await deps.emit({
    type: "run:init",
    task: job.task ?? "",
    mode: "swarm",
    agents: job.agents.map((a) => ({ id: a.id, branch: `agent/${a.id}`, intent: a.intent ?? "" })),
  });
  await deps.emit({ type: "log", line: `snapshotting base + ${job.agents.length} fork(s)` });

  // --- io zone: assemble the prepared bundle (holds tokens) ---
  const snap = await deps.io.snapshot({ base: job.base, agents: job.agents });
  await deps.emit({ type: "log", line: `prepared bundle ready (baseline ${short(snap.baselineSha)})` });
  await deps.record?.("snapshot", {
    baselineSha: snap.baselineSha,
    agents: String(job.agents.length),
    branches: Object.entries(snap.branches)
      .map(([a, s]) => `${a}=${s}`)
      .sort()
      .join(","),
  });

  // --- compute zone: merge + run tests (NO token, NO egress) ---
  await deps.emit({ type: "merge:start", scope: "all" });
  const merged = await deps.compute.integrate({
    bundle: snap.bundle,
    agents: job.agents.map((a) => ({ id: a.id, intent: a.intent })),
    task: job.task,
    resolver: job.resolver,
    tree: job.tree,
  });
  await deps.emit({
    type: "verify",
    scope: "final",
    passed: merged.green,
    summary: verdictSummary(merged.verdict, merged.summary),
  });
  await deps.record?.("verify", {
    verdict: merged.verdict,
    green: String(merged.green),
    baseSha: merged.baseSha,
    mergedSha: merged.mergedSha,
    ...Object.fromEntries(Object.entries(merged.summary).map(([k, v]) => [k, String(v)])),
  });

  if (!merged.green) {
    const status: IntegrationStatus = merged.verdict === "incompatible" ? "incompatible" : "failed";
    await deps.emit({ type: "run:done", green: false, finalTest: merged.verdict, mergedSha: "", summary: merged.summary });
    return { status, verdict: merged.verdict };
  }

  // --- io zone: CAS publish the merged bundle (holds the base write token) ---
  try {
    const pub = await deps.io.publish({
      base: job.base,
      mergedBundle: merged.mergedBundle ?? "",
      baseSha: merged.baseSha,
      mergedSha: merged.mergedSha,
      ref: merged.ref,
    });
    if (deps.recordPush) await deps.recordPush(pub.pushedSha);
    await deps.record?.("publish", { pushedSha: pub.pushedSha, baseSha: merged.baseSha });
    await deps.emit({ type: "run:done", green: true, finalTest: "published", mergedSha: pub.pushedSha, summary: merged.summary });
    return { status: "succeeded", verdict: merged.verdict, mergedSha: merged.mergedSha, pushedSha: pub.pushedSha };
  } catch (e) {
    if (e instanceof BaseMovedError) {
      await deps.emit({ type: "log", line: "publish rejected: base moved (CAS) — needs rebase/retry" });
      return { status: "base_moved", verdict: merged.verdict, mergedSha: merged.mergedSha };
    }
    throw e;
  }
}

function verdictSummary(verdict: string, summary: Record<string, number>): string {
  const merged = summary["agentsMerged"] ?? 0;
  return `${verdict} — ${merged} agent(s) merged`;
}

function short(sha: string): string {
  return sha.length > 10 ? sha.slice(0, 10) : sha;
}

// --- C9: self-trigger guard -------------------------------------------------

/**
 * isSelfPush reports whether an inbound change event names a commit Braid itself
 * just published, so the control plane can drop it instead of spawning a new run
 * from its own merge (GO_PRODUCTION_PLAN.md C9). The set of published SHAs is
 * kept per run/base by the control DO; this is the pure predicate over it.
 */
export function isSelfPush(publishedShas: Iterable<string>, incomingSha: string): boolean {
  if (!incomingSha) return false;
  for (const s of publishedShas) {
    if (s === incomingSha) return true;
  }
  return false;
}

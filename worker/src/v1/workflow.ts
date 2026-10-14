/**
 * IntegrationWorkflow is the durable consumer for Phase 3c (GO_PRODUCTION_PLAN.md
 * §3). POST /v1/runs starts one instance per run; it drives the two-zone pipeline
 * to completion with Workflow-level durability and updates the run's lifecycle
 * record + event stream as it goes.
 *
 * The orchestration logic lives in the pure runIntegration (unit-tested with
 * fakes). Durability is layered on by wrapping each zone call in a step.do, so a
 * transient container/network failure retries that step alone without redoing the
 * merge. Events are signed with the run's per-run HMAC secret (C10) before they
 * reach the Orchestrator DO.
 */

import { WorkflowEntrypoint, type WorkflowEvent, type WorkflowStep } from "cloudflare:workers";
import { ContainerZoneClient, type ContainerNamespace } from "./zones";
import { BaseMovedError, type IntegrateResponse, type PublishResponse, type SnapshotResponse } from "./ztypes";
import { BaseCoordinatorClient, type CoordinatorNamespace } from "./coordinator-do";
import {
  runIntegration,
  type IntegrationJob,
  type IntegrationOutcome,
  type RunEvent,
} from "./integration";
import { sign, SIGNATURE_HEADER } from "./hmac";
import { applyAction, type Run, type RunAction } from "./lifecycle";
import { Ed25519Signer, LedgerWriter, R2LedgerStore, type R2Like } from "./ledger";

/** The bindings the Workflow needs. */
export interface WorkflowEnv {
  COMPUTE_ZONE: ContainerNamespace;
  IO_ZONE: ContainerNamespace;
  BASE_COORDINATOR: CoordinatorNamespace;
  ORCHESTRATOR: {
    idFromName(name: string): unknown;
    get(id: unknown): { fetch(req: Request): Promise<Response> };
  };
  /** R2 bucket for the signed provenance ledger (Phase 4b). */
  LEDGER?: R2Like;
  /** Hex Ed25519 seed that signs ledger records. Unset -> ledger disabled. */
  LEDGER_SIGNING_SEED?: string;
}

/** Lease TTL for a publish: long enough to clone + fetch-bundle + CAS-push. */
const PUBLISH_LEASE_TTL_SECONDS = 300;

const DO_BASE = "https://do";
const stepRetry = { retries: { limit: 3, delay: "5 seconds" as const, backoff: "exponential" as const } };

export class IntegrationWorkflow extends WorkflowEntrypoint<WorkflowEnv, IntegrationJob> {
  async run(event: WorkflowEvent<IntegrationJob>, step: WorkflowStep): Promise<IntegrationOutcome> {
    const job = event.payload;
    const io = new ContainerZoneClient(this.env.IO_ZONE, `io-${job.runId}`, true);
    const compute = new ContainerZoneClient(this.env.COMPUTE_ZONE, `compute-${job.runId}`, false);
    const coordinator = new BaseCoordinatorClient(this.env.BASE_COORDINATOR);
    // Ledger is optional: only active when both the R2 bucket and the signing
    // seed are configured. When off, record is a no-op (provenance is additive).
    const ledger =
      this.env.LEDGER && this.env.LEDGER_SIGNING_SEED
        ? new LedgerWriter(new R2LedgerStore(this.env.LEDGER), new Ed25519Signer(this.env.LEDGER_SIGNING_SEED))
        : null;

    // Claim the run: queued -> running. Idempotent — a Workflow replay re-runs this
    // step, and applyAction simply no-ops if the run already left queued.
    await step.do("lifecycle:start", stepRetry, async () => {
      await this.advance(job.runId, "start", "workflow picked up");
      return true;
    });

    // Each zone call is its own durable step so a transient failure retries just
    // that call. The tested runIntegration drives the sequence; the clients here
    // record their results into the Workflow's step state. step.do returns a
    // Serializable<T>; the casts restore the precise type (the shapes are plain
    // JSON, so the runtime values match).
    const outcome = await runIntegration(
      {
        io: {
          snapshot: (req) => step.do("io:snapshot", stepRetry, () => io.snapshot(req)) as Promise<SnapshotResponse>,
          // Publish is gated by the Base-Coordinator. begin acquires the base
          // lease + CAS on the parent SHA; it is its own step that retries only
          // while the base is BUSY (another run holds the lease). A stale base
          // (base_moved) is thrown from this plain wrapper so runIntegration maps
          // it to the base_moved outcome instead of retrying. On a granted lease,
          // io.publish + the coordinator commit run as one step (abort on failure).
          publish: async (req) => {
            const begin = await step.do("coord:begin", stepRetry, async () => {
              const r = await coordinator.begin(job.baseKey, job.runId, req.baseSha, PUBLISH_LEASE_TTL_SECONDS);
              if (r.status === "busy") throw new Error(`base ${job.baseKey} busy (held by ${r.holder}); retrying`);
              return r; // granted | base_moved
            });
            if (begin.status === "base_moved") throw new BaseMovedError();
            return step.do("io:publish", stepRetry, async () => {
              try {
                const pub = await io.publish(req);
                const commit = await coordinator.commit(job.baseKey, job.runId, pub.pushedSha);
                if (commit.status !== "ok") throw new Error(`coordinator commit rejected: ${commit.status}`);
                return pub;
              } catch (e) {
                await coordinator.abort(job.baseKey, job.runId);
                throw e;
              }
            }) as Promise<PublishResponse>;
          },
        },
        compute: {
          integrate: (req) =>
            step.do("compute:integrate", stepRetry, () => compute.integrate(req)) as Promise<IntegrateResponse>,
        },
        emit: async (e: RunEvent) => {
          await step.do(`emit:${e.type}`, stepRetry, async () => {
            await this.postEvent(job, e);
            return true;
          });
        },
        recordPush: async (sha: string) => {
          await step.do("record:push", stepRetry, async () => {
            await this.recordPush(job.runId, sha);
            return true;
          });
        },
        record: ledger
          ? async (kind, payload) => {
              await step.do(`ledger:${kind}`, stepRetry, async () => {
                await ledger.append(job.runId, kind, payload);
                return true;
              });
            }
          : undefined,
        now: () => new Date().toISOString(),
      },
      job,
    );

    // Reconcile the lifecycle record with the outcome (succeeded/failed/...).
    await step.do("lifecycle:finalize", stepRetry, async () => {
      const action = outcomeToAction(outcome.status);
      if (action) {
        await this.advance(job.runId, action, outcome.verdict, {
          verdict: outcome.verdict,
          ...(outcome.pushedSha ? { mergedSha: outcome.pushedSha } : {}),
        });
      }
      return true;
    });
    return outcome;
  }

  /** postEvent signs the event body with the run's secret and posts it to the DO. */
  private async postEvent(job: IntegrationJob, event: RunEvent): Promise<void> {
    const body = JSON.stringify(event);
    const sig = await sign(job.eventSecret, body);
    const res = await this.stub(job.runId).fetch(
      new Request(`${DO_BASE}/runs/${job.runId}/event`, {
        method: "POST",
        headers: { "content-type": "application/json", [SIGNATURE_HEADER]: sig },
        body,
      }),
    );
    if (!res.ok) throw new Error(`postEvent ${event.type}: ${res.status}`);
  }

  private async recordPush(runId: string, sha: string): Promise<void> {
    const res = await this.stub(runId).fetch(
      new Request(`${DO_BASE}/runs/${runId}/published`, { method: "POST", body: JSON.stringify({ sha }) }),
    );
    if (!res.ok) throw new Error(`recordPush: ${res.status}`);
  }

  /**
   * advance applies a lifecycle action to the stored run and persists it. An
   * illegal/terminal transition (e.g. the run was canceled, or a replay re-applies
   * an action) is a silent no-op, so steps stay idempotent. `extra` patches fields
   * like verdict/mergedSha onto the advanced record.
   */
  private async advance(runId: string, action: RunAction, note: string, extra?: Partial<Run>): Promise<void> {
    const got = await this.stub(runId).fetch(new Request(`${DO_BASE}/runs/${runId}/record`));
    if (!got.ok) return; // run record gone; nothing to reconcile
    const { run } = (await got.json()) as { run: Run };
    const res = applyAction(run, action, new Date().toISOString(), note);
    if (!res.ok) return; // illegal/terminal — leave it
    const finalRun: Run = { ...res.run, ...(extra ?? {}) };
    await this.stub(runId).fetch(
      new Request(`${DO_BASE}/runs/${runId}/record`, { method: "PUT", body: JSON.stringify(finalRun) }),
    );
  }

  private stub(runId: string) {
    return this.env.ORCHESTRATOR.get(this.env.ORCHESTRATOR.idFromName(runId));
  }
}

/** outcomeToAction maps an integration outcome to the lifecycle action to apply. */
export function outcomeToAction(status: IntegrationOutcome["status"]): RunAction | null {
  switch (status) {
    case "succeeded":
      return "succeed";
    case "failed":
      return "fail";
    case "incompatible":
      return "incompatible";
    case "base_moved":
      return null; // retryable; the run stays running for a rebase (Phase 4)
    default:
      return null;
  }
}

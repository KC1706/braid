/**
 * Braid run events — the structured stream the orchestrator Durable Object
 * reduces into live dashboard state. Both the local demo and the live swarm
 * emit the same events, so the dashboard looks identical whether the pipeline
 * runs offline or on real Cloudflare Artifacts.
 */

export type AgentStatus = "spawned" | "forked" | "coding" | "pushed";
export type MergeStatus = "waiting" | "merging" | "auto-merged" | "conflict" | "resolved" | "verified";

export type BraidEvent =
  | { type: "run:init"; task: string; mode: "local" | "live"; agents: { id: string; branch: string; intent: string }[] }
  | { type: "base:ready"; name: string; remote: string }
  | { type: "agent:status"; agent: string; status: AgentStatus; detail?: string }
  | { type: "merge:start"; agent: string }
  | { type: "merge:auto"; agent: string; files: string[] }
  | { type: "conflict:detected"; agent: string; files: string[] }
  | { type: "conflict:attempt"; agent: string; path: string; attempt: number; strategy: string }
  | { type: "conflict:resolved"; agent: string; path: string; strategy: string; attempts: number; verified: string }
  | { type: "verify"; scope: string; passed: boolean; summary: string; verdict?: "green" | "red" | "inconclusive" | "incompatible"; rungs?: { rung: number; name: string; passed: boolean; summary: string }[] }
  | { type: "incompatible"; agent: string; reason: string; action: "escalate" | "drop"; dropped?: string; note: string }
  | { type: "run:done"; green: boolean; finalTest: string; mergedSha: string; summary: Record<string, number>; usersFile?: string }
  | { type: "log"; line: string };

/**
 * Best-effort emitter: POSTs each event to the orchestrator DO. Fire-and-forget
 * so instrumentation never slows down or breaks the pipeline — if the worker is
 * unreachable, the pipeline still runs to completion, just without the live view.
 */
export class BraidEmitter {
  private queue: Promise<unknown> = Promise.resolve();
  readonly dashboardUrl: string | null;

  constructor(
    private readonly workerUrl: string | undefined,
    readonly runId: string,
  ) {
    this.dashboardUrl = workerUrl ? `${workerUrl}/?run=${runId}` : null;
  }

  emit(event: BraidEvent): void {
    if (!this.workerUrl) return;
    const body = JSON.stringify(event);
    // Chain so events arrive in order; swallow errors (best-effort telemetry).
    this.queue = this.queue
      .then(() =>
        fetch(`${this.workerUrl}/runs/${this.runId}/event`, {
          method: "POST",
          headers: { "content-type": "application/json" },
          body,
        }).catch(() => {}),
      )
      .catch(() => {});
  }

  /** Await all in-flight posts (call before the process exits). */
  async flush(): Promise<void> {
    await this.queue.catch(() => {});
  }
}

/** A no-op emitter used when no worker URL is configured. */
export const nullEmitter = new BraidEmitter(undefined, "local");

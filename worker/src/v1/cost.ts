/**
 * Per-run cost metering (GO_PRODUCTION_PLAN.md §3 Phase 5c — economics).
 *
 * A PURE function from a run's observable facts (wall-clock span, zone compute
 * seconds, AI conflict calls, bytes published) to a $/run estimate, plus the
 * margin against a published price. No I/O, no clock — the caller supplies the
 * facts, so this unit-tests with fixtures and the console can show the same math.
 *
 * The price sheet is the documented Cloudflare Workers/Containers/AI/R2 pricing
 * (paid plan, USD), kept here as named constants so a change is one edit and the
 * published $/run is auditable. These are order-of-magnitude list prices for the
 * economics gate ("$/run published and positive"), not a billing system.
 */

/** PriceSheet is the per-unit cost model (USD). Overridable for what-if analysis. */
export interface PriceSheet {
  /** Container vCPU-second (the two zones are billed on active compute time). */
  computeVcpuSec: number;
  /** Container GiB-second of memory. */
  computeGiBSec: number;
  /** One Workers AI conflict-resolution call (per-neuron, averaged per call). */
  aiCall: number;
  /** One Workers request (control-plane + DO + Workflow steps, averaged). */
  request: number;
  /** R2 class-A op (ledger/event blob writes). */
  r2WriteOp: number;
  /** GiB published to the base (egress is free on R2; this is storage churn). */
  publishedGiB: number;
}

/** DEFAULT_PRICES — documented Cloudflare paid-plan list prices (USD), 2025-era. */
export const DEFAULT_PRICES: PriceSheet = {
  // Containers: ~$0.000020/vCPU-s and ~$0.0000025/GiB-s (standard pricing).
  computeVcpuSec: 0.000020,
  computeGiBSec: 0.0000025,
  // Workers AI: ~$0.011 per 1k neurons; a 70B conflict merge averages ~1k neurons.
  aiCall: 0.011,
  // Workers requests: $0.30 / million.
  request: 0.0000003,
  // R2 class-A: $4.50 / million ops.
  r2WriteOp: 0.0000045,
  // R2 storage: $0.015 / GB-month; per-run churn is negligible but modeled.
  publishedGiB: 0.015,
};

/** RunFacts are the observable resource facts of one completed run. */
export interface RunFacts {
  /** Compute-zone active time (merge + go test), vCPU-seconds. */
  computeVcpuSec: number;
  /** Compute-zone memory-time, GiB-seconds. */
  computeGiBSec: number;
  /** IO-zone active time (snapshot + publish), vCPU-seconds. */
  ioVcpuSec: number;
  /** IO-zone memory-time, GiB-seconds. */
  ioGiBSec: number;
  /** Workers AI conflict-resolution calls made this run. */
  aiCalls: number;
  /** Worker requests attributable to the run (control + DO + WF + events). */
  requests: number;
  /** R2 writes (ledger records + offloaded event blobs). */
  r2Writes: number;
  /** Bytes published to the base (merged tree), for storage churn. */
  publishedBytes: number;
}

export interface CostLine {
  item: string;
  qty: number;
  unit: string;
  unitUsd: number;
  usd: number;
}

export interface CostEstimate {
  /** Itemized lines (sorted most-expensive first). */
  lines: CostLine[];
  /** Total cost to run, USD. */
  costUsd: number;
  /** Published price per run, USD (what Braid would charge). */
  priceUsd: number;
  /** priceUsd - costUsd. The gate requires this positive. */
  marginUsd: number;
  /** marginUsd / priceUsd, in [−∞, 1]. */
  marginPct: number;
}

const GiB = 1024 * 1024 * 1024;

/**
 * estimateCost folds a run's facts through the price sheet into an itemized
 * $/run. `priceUsd` is the published per-run price; margin = price − cost.
 */
export function estimateCost(facts: RunFacts, priceUsd: number, prices: PriceSheet = DEFAULT_PRICES): CostEstimate {
  const lines: CostLine[] = [
    line("compute vCPU", facts.computeVcpuSec + facts.ioVcpuSec, "vCPU-s", prices.computeVcpuSec),
    line("compute memory", facts.computeGiBSec + facts.ioGiBSec, "GiB-s", prices.computeGiBSec),
    line("AI conflict merges", facts.aiCalls, "calls", prices.aiCall),
    line("worker requests", facts.requests, "reqs", prices.request),
    line("R2 writes", facts.r2Writes, "ops", prices.r2WriteOp),
    line("published storage", facts.publishedBytes / GiB, "GiB", prices.publishedGiB),
  ];
  lines.sort((a, b) => b.usd - a.usd);
  const costUsd = round(lines.reduce((s, l) => s + l.usd, 0));
  const marginUsd = round(priceUsd - costUsd);
  return {
    lines,
    costUsd,
    priceUsd: round(priceUsd),
    marginUsd,
    marginPct: priceUsd > 0 ? round(marginUsd / priceUsd) : 0,
  };
}

function line(item: string, qty: number, unit: string, unitUsd: number): CostLine {
  return { item, qty: round(qty), unit, unitUsd, usd: round(qty * unitUsd) };
}

/** round to 6 decimals — sub-cent precision without float dust in the JSON. */
function round(n: number): number {
  return Math.round(n * 1e6) / 1e6;
}

/**
 * factsFromRun derives a RunFacts estimate from a run's event log + lifecycle
 * record when exact meters are not wired. It is deliberately conservative: real
 * meters (container runtime, AI neuron counts) replace these when available.
 * `wallMs` is updatedAt − createdAt; `agents` scales the swarm's work.
 */
export function factsFromRun(input: {
  wallMs: number;
  agents: number;
  aiCalls: number;
  requests: number;
  r2Writes: number;
  publishedBytes: number;
}): RunFacts {
  const wallSec = Math.max(0, input.wallMs) / 1000;
  // Compute runs ~70% of wall at 1 vCPU / 6 GiB (standard-2); io ~15% at 1/4 vCPU / 1 GiB.
  const computeSec = wallSec * 0.7;
  const ioSec = wallSec * 0.15;
  return {
    computeVcpuSec: computeSec * 1,
    computeGiBSec: computeSec * 6,
    ioVcpuSec: ioSec * 0.25,
    ioGiBSec: ioSec * 1,
    aiCalls: input.aiCalls,
    requests: input.requests,
    r2Writes: input.r2Writes,
    publishedBytes: input.publishedBytes,
  };
}

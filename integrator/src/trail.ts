/** The "why" record — Braid's answer to "track not just what changed, but why". */
export interface ConflictRecord {
  path: string;
  /** Common-ancestor text for the hunk. */
  base: string;
  candidates: { agent: string; intent: string; content: string }[];
  resolution: string;
  rationale: string;
  strategy: "union" | "llm";
  verified: string;
  attempts: number;
}

export interface AgentMergeRecord {
  agent: string;
  branch: string;
  intent: string;
  autoMergedFiles: string[];
  conflictedFiles: string[];
  testResult: string;
  /** Set when this agent was dropped by the incompatibility policy. */
  dropped?: boolean;
  dropReason?: string;
}

/**
 * Recorded when an agent could not be composed with the integrated set
 * (PRODUCTION_PLAN.md §5.1 incompatibility path, C4). Preserves BOTH candidates
 * and the reason — nothing is silently lost — plus the policy's decision.
 */
export interface IncompatibilityRecord {
  agent: string;
  intent: string;
  /** Intents already integrated (the side it conflicts with). */
  withIntents: string[];
  reason: string;
  file?: string;
  /** Tail of the verification failure that proved the incompatibility. */
  detail?: string;
  action: "escalate" | "drop";
  /** For "drop": which agent was discarded. */
  dropped?: string;
  note: string;
}

/** Outcome of one verification-ladder run (PRODUCTION_PLAN.md §5.1). */
export interface VerificationRecord {
  verdict: "green" | "red" | "inconclusive" | "incompatible";
  summary: string;
  rungs: { rung: number; name: string; passed: boolean; summary: string }[];
}

export interface Trail {
  task: string;
  createdAt: string;
  baseline: string;
  mergedSha: string;
  agents: AgentMergeRecord[];
  conflicts: ConflictRecord[];
  /** Agents that could not be composed (empty on a fully-composed run). */
  incompatibilities?: IncompatibilityRecord[];
  /** Overall run verdict: "green" | "incompatible" | "red". */
  verdict?: "green" | "red" | "inconclusive" | "incompatible";
  finalTest: string;
  /** The deciding ladder verdict for the final merged tree. */
  finalVerification?: VerificationRecord;
  summary: {
    agentsMerged: number;
    filesAutoMerged: number;
    conflictsResolved: number;
    llmResolutions: number;
  };
}

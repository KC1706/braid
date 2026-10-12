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

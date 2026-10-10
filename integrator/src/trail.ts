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

export interface Trail {
  task: string;
  createdAt: string;
  baseline: string;
  mergedSha: string;
  agents: AgentMergeRecord[];
  conflicts: ConflictRecord[];
  finalTest: string;
  summary: {
    agentsMerged: number;
    filesAutoMerged: number;
    conflictsResolved: number;
    llmResolutions: number;
  };
}

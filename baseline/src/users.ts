import { db, type User } from "./db.js";

// Baseline: the single function two agents will both edit.
// Agent 1 (validation) and Agent 2 (rate limiting) both touch getUser(),
// which produces the guaranteed true-conflict hunk the merge engine resolves.
export function getUser(id: string): User | undefined {
  return db.find(id);
}

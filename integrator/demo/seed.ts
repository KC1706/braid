import { mkdirSync, writeFileSync, rmSync } from "node:fs";
import { join } from "node:path";
import { gitOk } from "../src/git.js";

/**
 * Builds a self-contained throwaway git repo that reproduces the plan's worked
 * example: a baseline Hono API, then three agent branches — two that both edit
 * getUser() (guaranteed true conflict) and one docs-only branch (clean
 * deterministic auto-merge). This is the fixture the integrator runs against.
 */
export interface SeedResult {
  repoDir: string;
  baseline: string;
  agents: { id: string; branch: string; intent: string }[];
}

const FILES_BASELINE: Record<string, string> = {
  "package.json": JSON.stringify(
    {
      name: "braid-demo-target",
      version: "0.0.0",
      private: true,
      type: "module",
      scripts: { test: "vitest run" },
    },
    null,
    2,
  ),
  "vitest.config.ts": `import { defineConfig } from "vitest/config";\nexport default defineConfig({ test: { include: ["test/**/*.test.ts"] } });\n`,
  "README.md": `# Braid demo target\n\nA tiny Hono API the agent swarm improves.\n\n## Routes\n- \`GET /users/:id\`\n`,
  "src/db.ts": `export interface User { id: string; name: string; email: string; }\n\nconst USERS: Record<string, User> = {\n  "1": { id: "1", name: "Ada Lovelace", email: "ada@braid.dev" },\n  "2": { id: "2", name: "Alan Turing", email: "alan@braid.dev" },\n};\n\nexport const db = {\n  find(id: string): User | undefined {\n    return USERS[id];\n  },\n};\n`,
  "src/users.ts": `import { db, type User } from "./db.js";\n\nexport function getUser(id: string): User | undefined {\n  return db.find(id);\n}\n`,
  "src/index.ts": `import { Hono } from "hono";\nimport { getUser } from "./users.js";\n\nconst app = new Hono();\napp.get("/users/:id", (c) => {\n  const user = getUser(c.req.param("id"));\n  if (!user) return c.json({ error: "not found" }, 404);\n  return c.json(user);\n});\nexport default app;\n`,
  "test/users.test.ts": `import { describe, it, expect } from "vitest";\nimport app from "../src/index.js";\n\ndescribe("GET /users/:id", () => {\n  it("returns a known user", async () => {\n    const res = await app.request("/users/1");\n    expect(res.status).toBe(200);\n    expect(await res.json()).toMatchObject({ id: "1", name: "Ada Lovelace" });\n  });\n  it("404s an unknown user", async () => {\n    const res = await app.request("/users/999");\n    expect(res.status).toBe(404);\n  });\n});\n`,
};

// Agent 1 — input validation. Edits getUser() + adds validate.ts.
const USERS_VALIDATION = `import { db, type User } from "./db.js";
import { validateId } from "./validate.js";

export function getUser(id: string): User | undefined {
  validateId(id);
  return db.find(id);
}
`;
const VALIDATE_TS = `export function validateId(id: string): void {
  if (!id || typeof id !== "string") {
    throw new Error("id required");
  }
}
`;

// Agent 2 — rate limiting. ALSO edits getUser() + adds ratelimit.ts.
const USERS_RATELIMIT = `import { db, type User } from "./db.js";
import { rateLimit } from "./ratelimit.js";

export function getUser(id: string): User | undefined {
  rateLimit(id);
  return db.find(id);
}
`;
const RATELIMIT_TS = `const hits = new Map<string, number>();

export function rateLimit(id: string): void {
  const n = (hits.get(id) ?? 0) + 1;
  hits.set(id, n);
  if (n > 100) throw new Error("rate limit exceeded");
}
`;

// Agent 3 — docs only. Non-overlapping -> git auto-merges with zero AI.
const README_DOCS = `# Braid demo target

A tiny Hono API the agent swarm improves.

## Routes
- \`GET /users/:id\`

## Hardening
This service validates input and rate-limits requests. (added by the docs agent)
`;

async function writeAll(dir: string, files: Record<string, string>) {
  for (const [path, content] of Object.entries(files)) {
    const full = join(dir, path);
    mkdirSync(join(full, ".."), { recursive: true });
    writeFileSync(full, content);
  }
}

export async function seed(repoDir: string): Promise<SeedResult> {
  rmSync(repoDir, { recursive: true, force: true });
  mkdirSync(repoDir, { recursive: true });

  await gitOk(repoDir, ["init", "-q"]);
  await gitOk(repoDir, ["config", "user.email", "braid@example.com"]);
  await gitOk(repoDir, ["config", "user.name", "Braid"]);
  await gitOk(repoDir, ["checkout", "-b", "baseline"]);

  await writeAll(repoDir, FILES_BASELINE);
  await gitOk(repoDir, ["add", "-A"]);
  await gitOk(repoDir, ["commit", "-q", "-m", "baseline: Hono /users API"]);

  // Agent 1
  await gitOk(repoDir, ["checkout", "-q", "-b", "agent/validation", "baseline"]);
  await writeAll(repoDir, { "src/users.ts": USERS_VALIDATION, "src/validate.ts": VALIDATE_TS });
  await gitOk(repoDir, ["add", "-A"]);
  await gitOk(repoDir, ["commit", "-q", "-m", "agent-1: input validation on getUser"]);

  // Agent 2
  await gitOk(repoDir, ["checkout", "-q", "-b", "agent/ratelimit", "baseline"]);
  await writeAll(repoDir, { "src/users.ts": USERS_RATELIMIT, "src/ratelimit.ts": RATELIMIT_TS });
  await gitOk(repoDir, ["add", "-A"]);
  await gitOk(repoDir, ["commit", "-q", "-m", "agent-2: rate limiting on getUser"]);

  // Agent 3
  await gitOk(repoDir, ["checkout", "-q", "-b", "agent/docs", "baseline"]);
  await writeAll(repoDir, { "README.md": README_DOCS });
  await gitOk(repoDir, ["add", "-A"]);
  await gitOk(repoDir, ["commit", "-q", "-m", "agent-3: document hardening"]);

  await gitOk(repoDir, ["checkout", "-q", "baseline"]);

  return {
    repoDir,
    baseline: "baseline",
    agents: [
      { id: "agent-1", branch: "agent/validation", intent: "input validation" },
      { id: "agent-2", branch: "agent/ratelimit", intent: "rate limiting" },
      { id: "agent-3", branch: "agent/docs", intent: "documentation" },
    ],
  };
}

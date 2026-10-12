import { describe, it, expect } from "vitest";
import { fileURLToPath } from "node:url";
import { dirname, resolve, join } from "node:path";
import { mkdirSync, writeFileSync, rmSync } from "node:fs";
import { analyzeModule, semanticConflicts } from "../src/semantic.js";

const __dirname = dirname(fileURLToPath(import.meta.url));

function makeRepo(files: Record<string, string>): string {
  const repoDir = resolve(__dirname, "../.tmp/semantic-repo-" + Math.random().toString(36).slice(2));
  rmSync(repoDir, { recursive: true, force: true });
  for (const [path, content] of Object.entries(files)) {
    const full = join(repoDir, path);
    mkdirSync(dirname(full), { recursive: true });
    writeFileSync(full, content);
  }
  return repoDir;
}

describe("analyzeModule (TS parser, structural)", () => {
  it("extracts named and default exports", () => {
    const m = analyzeModule("x.ts", `export function a(){}\nexport const b = 1;\nexport default class C {}\ninterface I {}\nexport type T = number;`);
    expect([...m.exports].sort()).toEqual(["T", "a", "b"]);
    expect(m.hasDefaultExport).toBe(true);
  });
  it("reads import bindings", () => {
    const m = analyzeModule("x.ts", `import d, { a, b as c } from "./y.js";\nimport * as ns from "./z.js";`);
    expect(m.imports[0]).toMatchObject({ moduleSpecifier: "./y.js", named: ["a", "b"], wantsDefault: true });
    expect(m.imports[1]).toMatchObject({ wantsNamespace: true });
  });
});

describe("semanticConflicts (Rung 3) — C1 textual-clean-but-broken merge", () => {
  it("no conflict when every imported binding exists", () => {
    const repo = makeRepo({
      "src/a.ts": `export function foo(){ return 1; }\n`,
      "src/b.ts": `import { foo } from "./a.js";\nexport const x = foo();\n`,
    });
    const r = semanticConflicts(repo);
    expect(r.conflicts).toHaveLength(0);
  });

  it("detects a dangling reference after a rename-across-forks merge", () => {
    // Simulates: fork A renamed foo -> bar in a.ts; fork B still imports foo in b.ts.
    // git merges both textually clean, but the import now resolves to nothing.
    const repo = makeRepo({
      "src/a.ts": `export function bar(){ return 1; }\n`,
      "src/b.ts": `import { foo } from "./a.js";\nexport const x = foo();\n`,
    });
    const r = semanticConflicts(repo);
    expect(r.conflicts).toHaveLength(1);
    expect(r.conflicts[0]).toMatchObject({ fromFile: "src/b.ts", name: "foo", kind: "missing-export" });
  });

  it("detects a missing default import", () => {
    const repo = makeRepo({
      "src/a.ts": `export const foo = 1;\n`, // no default export
      "src/b.ts": `import whatever from "./a.js";\nexport const x = whatever;\n`,
    });
    const r = semanticConflicts(repo);
    expect(r.conflicts[0]).toMatchObject({ name: "default", kind: "missing-default" });
  });

  it("follows export-star re-exports", () => {
    const repo = makeRepo({
      "src/impl.ts": `export function foo(){ return 1; }\n`,
      "src/a.ts": `export * from "./impl.js";\n`,
      "src/b.ts": `import { foo } from "./a.js";\nexport const x = foo();\n`,
    });
    const r = semanticConflicts(repo);
    expect(r.conflicts).toHaveLength(0);
  });

  it("ignores bare (node_modules) specifiers and namespace imports", () => {
    const repo = makeRepo({
      "src/b.ts": `import { Hono } from "hono";\nimport * as a from "./a.js";\nexport const x = a;\n`,
      "src/a.ts": `export const y = 1;\n`,
    });
    const r = semanticConflicts(repo);
    expect(r.conflicts).toHaveLength(0);
  });
});

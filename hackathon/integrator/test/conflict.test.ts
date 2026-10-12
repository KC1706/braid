import { describe, it, expect } from "vitest";
import { parseConflicts, applyResolutions } from "../src/conflict.js";
import { UnionResolver } from "../src/resolver.js";

const SAMPLE = `export function getUser(id) {
<<<<<<< ours
  validateId(id);
  return db.find(id);
||||||| base
  return db.find(id);
=======
  rateLimit(id);
  return db.find(id);
>>>>>>> theirs
}`;

describe("parseConflicts", () => {
  it("extracts ours/base/theirs for a diff3 hunk", () => {
    const [h] = parseConflicts(SAMPLE);
    expect(h.ours).toEqual(["  validateId(id);", "  return db.find(id);"]);
    expect(h.base).toEqual(["  return db.find(id);"]);
    expect(h.theirs).toEqual(["  rateLimit(id);", "  return db.find(id);"]);
    expect(h.contextBefore).toContain("export function getUser(id) {");
    expect(h.contextAfter).toContain("}");
  });
});

describe("UnionResolver", () => {
  it("keeps both agents' additions around the shared base line", async () => {
    const [h] = parseConflicts(SAMPLE);
    const r = await new UnionResolver().resolve({
      path: "src/users.ts",
      hunk: h,
      oursIntent: "input validation",
      theirsIntent: "rate limiting",
      attempt: 0,
    });
    expect(r.strategy).toBe("union");
    expect(r.content).toBe("  validateId(id);\n  rateLimit(id);\n  return db.find(id);");
  });
});

describe("applyResolutions", () => {
  it("splices the resolution back into the file", async () => {
    const [h] = parseConflicts(SAMPLE);
    const r = await new UnionResolver().resolve({
      path: "x",
      hunk: h,
      oursIntent: "a",
      theirsIntent: "b",
      attempt: 0,
    });
    const merged = applyResolutions(SAMPLE, [r.content]);
    expect(merged).not.toContain("<<<<<<<");
    expect(merged).toContain("  validateId(id);\n  rateLimit(id);\n  return db.find(id);");
    expect(merged.startsWith("export function getUser(id) {")).toBe(true);
    expect(merged.trimEnd().endsWith("}")).toBe(true);
  });
});

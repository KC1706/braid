import ts from "typescript";
import { readFileSync, existsSync, readdirSync, statSync } from "node:fs";
import { join, dirname, relative, resolve as presolve } from "node:path";

/**
 * Test-file matcher kept local (not imported from ladder.ts) so semantic.ts and
 * ladder.ts don't form an import cycle — ladder depends on this module.
 */
const TEST_PATTERNS: RegExp[] = [
  /\.(test|spec)\.[cm]?[jt]sx?$/,
  /(^|\/)(test|tests|__tests__)\//,
];
function isTestFile(path: string, patterns = TEST_PATTERNS): boolean {
  return patterns.some((re) => re.test(path));
}

/**
 * Rung 3 — semantic-conflict detection (PRODUCTION_PLAN.md §5.1, fixes C1).
 *
 * The dangerous merge is the one that is *textually* clean but *semantically*
 * broken: agent A renames/removes a symbol, agent B references it from another
 * file, `git merge` reports no conflict, and the result compiles-shaped but is
 * wired to nothing. The deterministic textual union never sees this, and tests
 * do not always cover it. This analyzer builds a symbol graph of the merged
 * tree and flags cross-module references that resolve to nothing.
 *
 * It uses the real TypeScript parser (not regex) so imports/exports are read
 * structurally. It is deliberately conservative — it only flags *intra-repo*
 * relative imports whose named bindings are absent from the target module's
 * exports — so false positives are near-zero while the canonical rename-across-
 * forks break is caught.
 */

const SOURCE_EXT = /\.(c|m)?[jt]sx?$/;

export interface BrokenReference {
  /** File doing the importing. */
  fromFile: string;
  /** The unresolved imported binding. */
  name: string;
  /** Module specifier as written (e.g. "./users.js"). */
  moduleSpecifier: string;
  /** Resolved target file within the repo. */
  targetFile: string;
  kind: "missing-export" | "missing-default" | "missing-module";
}

export interface SemanticResult {
  conflicts: BrokenReference[];
  /** Files analyzed (non-test source files in the merged tree). */
  analyzed: number;
}

interface ModuleInfo {
  exports: Set<string>;
  hasDefaultExport: boolean;
  /** `export * from "x"` re-export specifiers, resolved lazily. */
  starReExports: string[];
  imports: ImportBinding[];
}

interface ImportBinding {
  moduleSpecifier: string;
  /** Named imports (`import { a, b }`), the local default (`import d`), or "*". */
  named: string[];
  wantsDefault: boolean;
  wantsNamespace: boolean;
}

function parse(fileName: string, text: string): ts.SourceFile {
  return ts.createSourceFile(fileName, text, ts.ScriptTarget.Latest, true);
}

/** Named + default exports declared by a module (structural, via the TS AST). */
export function analyzeModule(fileName: string, text: string): ModuleInfo {
  const sf = parse(fileName, text);
  const exports = new Set<string>();
  let hasDefaultExport = false;
  const starReExports: string[] = [];
  const imports: ImportBinding[] = [];

  const addExportModifier = (node: ts.Node, name?: string) => {
    const mods = ts.canHaveModifiers(node) ? ts.getModifiers(node) : undefined;
    if (!mods) return;
    const isExport = mods.some((m) => m.kind === ts.SyntaxKind.ExportKeyword);
    const isDefault = mods.some((m) => m.kind === ts.SyntaxKind.DefaultKeyword);
    if (!isExport) return;
    if (isDefault) hasDefaultExport = true;
    else if (name) exports.add(name);
  };

  sf.forEachChild((node) => {
    // export function/class/const/let/interface/type/enum ...
    if (ts.isFunctionDeclaration(node) || ts.isClassDeclaration(node)) {
      addExportModifier(node, node.name?.text);
    } else if (
      ts.isInterfaceDeclaration(node) ||
      ts.isTypeAliasDeclaration(node) ||
      ts.isEnumDeclaration(node)
    ) {
      addExportModifier(node, node.name.text);
    } else if (ts.isVariableStatement(node)) {
      const mods = ts.getModifiers(node);
      if (mods?.some((m) => m.kind === ts.SyntaxKind.ExportKeyword)) {
        for (const decl of node.declarationList.declarations) {
          if (ts.isIdentifier(decl.name)) exports.add(decl.name.text);
        }
      }
    } else if (ts.isExportAssignment(node)) {
      // export default <expr>  /  export = <expr>
      hasDefaultExport = true;
    } else if (ts.isExportDeclaration(node)) {
      if (node.exportClause && ts.isNamedExports(node.exportClause)) {
        for (const el of node.exportClause.elements) exports.add(el.name.text);
      } else if (!node.exportClause && node.moduleSpecifier && ts.isStringLiteral(node.moduleSpecifier)) {
        starReExports.push(node.moduleSpecifier.text); // export * from "..."
      }
    } else if (ts.isImportDeclaration(node) && ts.isStringLiteral(node.moduleSpecifier)) {
      const named: string[] = [];
      let wantsDefault = false;
      let wantsNamespace = false;
      const clause = node.importClause;
      if (clause?.name) wantsDefault = true; // import X from "..."
      const nb = clause?.namedBindings;
      if (nb && ts.isNamespaceImport(nb)) wantsNamespace = true; // import * as X
      if (nb && ts.isNamedImports(nb)) for (const el of nb.elements) named.push(el.propertyName?.text ?? el.name.text);
      imports.push({ moduleSpecifier: node.moduleSpecifier.text, named, wantsDefault, wantsNamespace });
    }
  });

  return { exports, hasDefaultExport, starReExports, imports };
}

/** Resolve a relative import specifier to a repo file, trying .ts/.tsx/.js and index. */
function resolveModule(fromFile: string, spec: string, repoDir: string): string | null {
  if (!spec.startsWith(".")) return null; // bare specifier -> node_modules, not ours
  const baseAbs = presolve(dirname(fromFile), spec);
  const candidates: string[] = [];
  const noExt = baseAbs.replace(/\.(c|m)?[jt]sx?$/i, "");
  // `./users.js` in NodeNext maps to ./users.ts — try swapping/adding extensions.
  for (const ext of [".ts", ".tsx", ".mts", ".cts", ".js", ".jsx", ".mjs", ".cjs"]) {
    candidates.push(noExt + ext);
  }
  candidates.push(baseAbs); // as-written (already has a real extension)
  for (const ext of [".ts", ".tsx", ".js", ".jsx"]) candidates.push(join(baseAbs, "index" + ext));
  for (const c of candidates) {
    if (existsSync(c) && statSync(c).isFile()) return c;
  }
  return null;
}

function walkSourceFiles(repoDir: string): string[] {
  const out: string[] = [];
  const skip = new Set(["node_modules", ".git", ".braid", "dist", "build", ".tmp"]);
  const rec = (dir: string) => {
    for (const entry of readdirSync(dir, { withFileTypes: true })) {
      if (skip.has(entry.name)) continue;
      const full = join(dir, entry.name);
      if (entry.isDirectory()) rec(full);
      else if (SOURCE_EXT.test(entry.name)) out.push(full);
    }
  };
  rec(repoDir);
  return out;
}

/**
 * Analyze the merged working tree for broken cross-module references. Test files
 * are excluded from the *importer* set (they legitimately import test helpers),
 * but they still count as modules that can be imported FROM.
 */
export function semanticConflicts(
  repoDir: string,
  testPatterns = TEST_PATTERNS,
): SemanticResult {
  const absFiles = walkSourceFiles(repoDir);
  const modules = new Map<string, ModuleInfo>();
  for (const abs of absFiles) {
    try {
      modules.set(abs, analyzeModule(abs, readFileSync(abs, "utf8")));
    } catch {
      /* unparseable file — leave to Rung 2/4 to surface */
    }
  }

  const exportsOf = (abs: string, seen = new Set<string>()): { names: Set<string>; hasDefault: boolean } => {
    const info = modules.get(abs);
    if (!info || seen.has(abs)) return { names: new Set(), hasDefault: false };
    seen.add(abs);
    const names = new Set(info.exports);
    let hasDefault = info.hasDefaultExport;
    for (const spec of info.starReExports) {
      const target = resolveModule(abs, spec, repoDir);
      if (target) {
        const re = exportsOf(target, seen);
        for (const n of re.names) names.add(n);
      }
    }
    return { names, hasDefault };
  };

  const conflicts: BrokenReference[] = [];
  for (const abs of absFiles) {
    const rel = relative(repoDir, abs);
    if (isTestFile(rel, testPatterns)) continue; // don't police test-file imports here
    const info = modules.get(abs);
    if (!info) continue;
    for (const imp of info.imports) {
      const target = resolveModule(abs, imp.moduleSpecifier, repoDir);
      if (!target) continue; // bare/external or unresolved-outside-repo: not our call
      const { names, hasDefault } = exportsOf(target);
      const relTarget = relative(repoDir, target);
      if (imp.wantsNamespace) continue; // namespace import can't dangle on a name
      if (imp.wantsDefault && !hasDefault) {
        conflicts.push({ fromFile: rel, name: "default", moduleSpecifier: imp.moduleSpecifier, targetFile: relTarget, kind: "missing-default" });
      }
      for (const name of imp.named) {
        if (!names.has(name)) {
          conflicts.push({ fromFile: rel, name, moduleSpecifier: imp.moduleSpecifier, targetFile: relTarget, kind: "missing-export" });
        }
      }
    }
  }

  return { conflicts, analyzed: absFiles.length };
}

// Package semantic is Braid's pure-Go symbol analysis for Rung 3: attribute a
// clean-but-broken merge to a renamed/removed symbol, then repair it. It is the
// Go analogue of the TS integrator's semantic.ts, reshaped for Go
// (GO_MIGRATION_PLAN.md §1.2–§1.3):
//
//   - In Go a dangling cross-package reference after a clean merge is a COMPILE
//     error, so detection is robustly done by the build rung (Rung 2). This
//     package's job is attribution (which symbol, where) and repair.
//   - Go has no symbol-level import aliasing, so a rename Foo→Bar is repaired by
//     rewriting call sites `a.Foo(...)` → `a.Bar(...)` via go/ast positions + a
//     text splice (stdlib only; no x/tools).
//
// The Analyzer interface keeps the design multi-language: GoAnalyzer ships today;
// a tree-sitter-backed analyzer can be added behind the same seam later (C22).
package semantic

import (
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
)

// ExportSet is a module's exported top-level identifiers (funcs, types, vars,
// consts). Names is sorted for deterministic output (GO_MIGRATION_PLAN.md §1.5).
type ExportSet struct {
	Names []string
}

// Has reports whether name is in the set.
func (e ExportSet) Has(name string) bool {
	for _, n := range e.Names {
		if n == name {
			return true
		}
	}
	return false
}

// ImportRef is one import in a file plus the exported selectors referenced
// through it (e.g. import "x/users" used as users.GetUser → Name="users",
// Selectors=["GetUser"]).
type ImportRef struct {
	Path      string
	Name      string // local package name (alias, or last path element)
	Selectors []string
}

// Analyzer is the language-pluggable seam. Only GoAnalyzer ships today.
type Analyzer interface {
	Lang() string
	Matches(path string) bool
	Exports(src string) (ExportSet, error)
	Imports(src string) ([]ImportRef, error)
}

// GoAnalyzer reads exports and imports structurally via go/parser + go/ast.
type GoAnalyzer struct{}

func (GoAnalyzer) Lang() string { return "go" }

func (GoAnalyzer) Matches(path string) bool {
	return strings.HasSuffix(path, ".go") && !strings.HasSuffix(path, "_test.go")
}

// Exports returns the exported top-level identifiers declared by src. Methods
// (funcs with a receiver) are not package-level selectors, so they are excluded.
func (GoAnalyzer) Exports(src string) (ExportSet, error) {
	f, err := parse(src)
	if err != nil {
		return ExportSet{}, err
	}
	seen := map[string]bool{}
	for _, decl := range f.Decls {
		switch d := decl.(type) {
		case *ast.FuncDecl:
			if d.Recv == nil && d.Name.IsExported() {
				seen[d.Name.Name] = true
			}
		case *ast.GenDecl:
			for _, spec := range d.Specs {
				switch s := spec.(type) {
				case *ast.TypeSpec:
					if s.Name.IsExported() {
						seen[s.Name.Name] = true
					}
				case *ast.ValueSpec:
					for _, n := range s.Names {
						if n.IsExported() {
							seen[n.Name] = true
						}
					}
				}
			}
		}
	}
	return ExportSet{Names: sortedKeys(seen)}, nil
}

// Imports returns the file's imports, each with the exported selectors used
// through that package's local name.
func (GoAnalyzer) Imports(src string) ([]ImportRef, error) {
	f, err := parse(src)
	if err != nil {
		return nil, err
	}
	// Map local package name -> import path.
	byName := map[string]string{}
	order := []string{}
	for _, imp := range f.Imports {
		path, _ := strconv.Unquote(imp.Path.Value)
		name := lastPathElem(path)
		if imp.Name != nil && imp.Name.Name != "_" && imp.Name.Name != "." {
			name = imp.Name.Name
		}
		if _, ok := byName[name]; !ok {
			order = append(order, name)
		}
		byName[name] = path
	}

	// Collect exported selectors referenced as <pkgName>.<Sel>.
	sels := map[string]map[string]bool{} // pkgName -> set of selector names
	ast.Inspect(f, func(n ast.Node) bool {
		se, ok := n.(*ast.SelectorExpr)
		if !ok {
			return true
		}
		x, ok := se.X.(*ast.Ident)
		if !ok {
			return true
		}
		if _, isImport := byName[x.Name]; !isImport {
			return true
		}
		if !se.Sel.IsExported() {
			return true
		}
		if sels[x.Name] == nil {
			sels[x.Name] = map[string]bool{}
		}
		sels[x.Name][se.Sel.Name] = true
		return true
	})

	refs := make([]ImportRef, 0, len(order))
	for _, name := range order {
		refs = append(refs, ImportRef{
			Path: byName[name], Name: name, Selectors: sortedKeys(sels[name]),
		})
	}
	return refs, nil
}

// BrokenRef is one attributed broken cross-package reference.
type BrokenRef struct {
	FromFile string // repo-relative file doing the referencing
	Name     string // the missing selector / identifier
	Pkg      string // the package local name it was referenced through ("" if same-package)
	Kind     string // "missing-export" | "missing-symbol"
}

// reUndefinedSelector matches `./file.go:L:C: undefined: pkg.Name`.
var reUndefinedSelector = regexp.MustCompile(`(?m)^(\S+\.go):\d+:\d+: undefined: (\w+)\.(\w+)`)

// reUndefinedSymbol matches same-package `./file.go:L:C: undefined: Name`.
var reUndefinedSymbol = regexp.MustCompile(`(?m)^(\S+\.go):\d+:\d+: undefined: (\w+)\s*$`)

// SemanticConflicts attributes a build failure to broken references by parsing
// the build diagnostics (the primary signal in Go). repoDir is accepted for
// symbolic parity with the TS signature and future AST cross-referencing; today
// attribution comes from the diagnostics alone. Output is sorted (determinism).
func SemanticConflicts(repoDir, buildDiag string) ([]BrokenRef, error) {
	seen := map[string]BrokenRef{}
	for _, m := range reUndefinedSelector.FindAllStringSubmatch(buildDiag, -1) {
		file, pkg, name := cleanFile(m[1]), m[2], m[3]
		key := file + "|" + pkg + "|" + name
		seen[key] = BrokenRef{FromFile: file, Name: name, Pkg: pkg, Kind: "missing-export"}
	}
	for _, m := range reUndefinedSymbol.FindAllStringSubmatch(buildDiag, -1) {
		file, name := cleanFile(m[1]), m[2]
		key := file + "||" + name
		if _, ok := seen[key]; !ok {
			seen[key] = BrokenRef{FromFile: file, Name: name, Kind: "missing-symbol"}
		}
	}
	out := make([]BrokenRef, 0, len(seen))
	for _, b := range seen {
		out = append(out, b)
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].FromFile != out[j].FromFile {
			return out[i].FromFile < out[j].FromFile
		}
		if out[i].Pkg != out[j].Pkg {
			return out[i].Pkg < out[j].Pkg
		}
		return out[i].Name < out[j].Name
	})
	return out, nil
}

// InferRename decides whether a dangling reference was caused by an unambiguous
// rename of a base symbol. It returns the new name iff the target module dropped
// exactly the missing symbol and gained exactly one new export — otherwise ok is
// false, so a genuine deletion is never papered over. Deterministic, no LLM.
func InferRename(baseSrc, nowSrc, missing string) (newName string, ok bool) {
	var a GoAnalyzer
	base, err := a.Exports(baseSrc)
	if err != nil {
		return "", false
	}
	now, err := a.Exports(nowSrc)
	if err != nil {
		return "", false
	}
	if !base.Has(missing) || now.Has(missing) {
		return "", false // not a rename of a base symbol, or still present
	}
	var added, removed []string
	for _, n := range now.Names {
		if !base.Has(n) {
			added = append(added, n)
		}
	}
	for _, n := range base.Names {
		if !now.Has(n) {
			removed = append(removed, n)
		}
	}
	if len(added) == 1 && len(removed) == 1 && removed[0] == missing {
		return added[0], true
	}
	return "", false
}

// RepairRename rewrites every call site `<pkg>.<old>` to `<pkg>.<new>` across the
// Go files in repoDir, in files that import a package whose local name is pkg.
// This is the Go repair primitive (selector rewrite, not an import alias). It
// returns the number of files changed.
func RepairRename(repoDir, pkg, oldName, newName string) (int, error) {
	var files []string
	err := filepath.WalkDir(repoDir, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			if skipDir(d.Name()) && path != repoDir {
				return filepath.SkipDir
			}
			return nil
		}
		if strings.HasSuffix(path, ".go") {
			files = append(files, path)
		}
		return nil
	})
	if err != nil {
		return 0, err
	}
	sort.Strings(files) // determinism

	changed := 0
	for _, path := range files {
		src, err := os.ReadFile(path)
		if err != nil {
			return changed, err
		}
		next, did, err := rewriteSelectors(string(src), pkg, oldName, newName)
		if err != nil {
			continue // unparseable file: leave to the build rung
		}
		if did {
			if err := os.WriteFile(path, []byte(next), 0o644); err != nil {
				return changed, err
			}
			changed++
		}
	}
	return changed, nil
}

// rewriteSelectors replaces `<pkg>.<old>` with `<pkg>.<new>` in src, but only
// when src imports a package with local name pkg. Positions come from go/ast; the
// edit is a reverse-order text splice so earlier offsets stay valid.
func rewriteSelectors(src, pkg, oldName, newName string) (string, bool, error) {
	fset := token.NewFileSet()
	f, err := parser.ParseFile(fset, "", src, parser.SkipObjectResolution)
	if err != nil {
		return src, false, err
	}
	imports := false
	for _, imp := range f.Imports {
		path, _ := strconv.Unquote(imp.Path.Value)
		name := lastPathElem(path)
		if imp.Name != nil {
			name = imp.Name.Name
		}
		if name == pkg {
			imports = true
			break
		}
	}
	if !imports {
		return src, false, nil
	}

	type span struct{ start, end int }
	var spans []span
	ast.Inspect(f, func(n ast.Node) bool {
		se, ok := n.(*ast.SelectorExpr)
		if !ok {
			return true
		}
		x, ok := se.X.(*ast.Ident)
		if !ok || x.Name != pkg || se.Sel.Name != oldName {
			return true
		}
		// fset offsets are 1-based positions; convert to byte offsets into src.
		spans = append(spans, span{fset.Position(se.Sel.Pos()).Offset, fset.Position(se.Sel.End()).Offset})
		return true
	})
	if len(spans) == 0 {
		return src, false, nil
	}
	sort.Slice(spans, func(i, j int) bool { return spans[i].start > spans[j].start }) // reverse
	b := []byte(src)
	for _, s := range spans {
		b = append(b[:s.start], append([]byte(newName), b[s.end:]...)...)
	}
	return string(b), true, nil
}

// --- helpers ---

func parse(src string) (*ast.File, error) {
	fset := token.NewFileSet()
	return parser.ParseFile(fset, "", src, parser.SkipObjectResolution)
}

func sortedKeys(m map[string]bool) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

func lastPathElem(p string) string {
	if i := strings.LastIndexByte(p, '/'); i >= 0 {
		return p[i+1:]
	}
	return p
}

// cleanFile normalizes a diagnostic path like "./caller.go" to "caller.go".
func cleanFile(p string) string {
	return strings.TrimPrefix(p, "./")
}

func skipDir(name string) bool {
	switch name {
	case "node_modules", ".git", ".braid", "dist", "build", ".tmp", "vendor":
		return true
	}
	return false
}

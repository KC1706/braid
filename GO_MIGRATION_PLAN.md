# Braid — Go Migration Plan (brain port)

> Reviewed, execution-ready plan for porting Braid's TypeScript "brain" to Go and laying the seams for
> the Keploy (verify) and Entire (provenance) migrations. Companion to `PRODUCTION_PLAN.md` (the C1–C22
> correctness design), `ARCHITECTURE.md` (target layout), and `status.md` (session handoff).
>
> Decisions locked: **single-language Go** platform; Keploy + Entire source migrated **in-tree** (no
> upstream); **pure-Go** semantic analysis (no cgo/tree-sitter for now); thin **TS edge** (`edge/`)
> fronting a fat Go core; module `github.com/KC1706/braid`, Go 1.26. The TS submission is frozen in
> `hackathon/` and must not be modified.

---

## 0. Status (as of 2026-10-12)

Done:
- `hackathon/` frozen snapshot (TS demo, runs standalone, baseline 2 + integrator 24 green).
- Go module skeleton + CI (`.github/workflows/go.yml`: build + vet + test).
- `pkg/gitx` — git plumbing ported + tested (`Run`/`Ok`/`CurrentSHA`/`ConflictedFiles`/`ChangedFiles`/
  `LsFiles`/`ShowFile`).

Next: execute §5 sub-chunks in order, each keeping `go test ./...` green and the TS `hackathon/`
untouched. Delete the old root TS workspaces only after Go reaches 24-test parity.

---

## 1. Constraints that reshape the port (read first)

1. **Go targets only, for now.** Pure-Go semantic analysis means the repos Braid merges are Go. The
   target fixtures are re-created as a small Go HTTP module (mirroring the Hono `getUser` conflict).
   The original TS target + true multi-language return later via the `semantic.Analyzer` interface and
   Keploy's multi-language coverage. **This is a conscious scoping tradeoff, not a silent regression.**
2. **Semantic detection in Go ≈ the build.** A dangling cross-package reference after a clean merge is
   a compile error. So we add a **build/typecheck rung** (Rung 2) that catches it robustly; Rung 3's
   distinct job becomes **attribute + repair** (base-vs-fork AST export-diff), not hand-rolled detection.
3. **Auto-repair is a selector rewrite, not an import alias.** Go has no symbol-level import aliasing,
   so a rename `Foo→Bar` means rewriting call sites `a.Foo(...)` → `a.Bar(...)` via `go/ast` positions +
   text splice (stdlib only; avoid `x/tools/astutil` to stay dep-light).
4. **Static test counting is weaker in Go.** `t.Skip()` sits inside a kept `func Test`, so a static
   counter can't see skip-based weakening (the TS `.skip` token trick could). Keep the static
   `func Test` count as a cheap guard, but the real coverage-regression guard is **Keploy `go test
   -cover` deltas** — a reason to pull the Keploy coverage migration forward.
5. **Determinism.** Go map iteration is randomized. Sort every map-derived slice (semantic refs, guard
   violations, export sets) so output and tests are stable (supports C6).
6. **Fixtures in `t.TempDir()`** — generate a self-contained nested Go module at test time (its own
   `go.mod`, auto-excluded from the parent build). Nothing committed; mirrors the old `seed.ts`.

---

## 2. Target layout (recap from ARCHITECTURE.md)

```
cmd/braid/            # CLI + sandbox entrypoint
internal/             # Braid's novel IP (private)
  merge/              # git 3-way driver + the agent loop     (conflict.ts + integrate.ts)
  ladder/             # verification ladder + Verdict + rungs  (ladder.ts)
  resolver/           # union + hybrid + LLM over HTTP         (resolver.ts)
  semantic/           # pure-Go symbol graph: detect + repair  (semantic.ts)
  policy/             # incompatibility policy                 (policy.ts)
pkg/
  gitx/   [DONE]      # git plumbing
  models/             # shared types + events
  verify/             # (later) MIGRATED from Keploy: coverage/testgen/replay/contract
  provenance/         # (later) MIGRATED from Entire: checkpoint/redact/attribution/memory
edge/                 # thin TS Worker + Orchestrator DO + static dashboard (the one non-Go corner)
sandbox/              # container image: braid binary + language runtimes
hackathon/            # FROZEN TS submission — do not modify
```

---

## 3. Package-by-package design (Go API sketches)

### `pkg/models` — shared types (no logic)
```go
type Verdict string // "green" | "red" | "inconclusive" | "incompatible"

type RungResult struct { Rung int; Name string; Passed bool; Summary, Detail string }
type LadderResult struct {
    Verdict Verdict; Passed bool; Summary, Output string; Rungs []RungResult
}

type ConflictRecord struct { Path, Base string; Candidates []Candidate
                             Resolution, Rationale, Strategy, Verified string; Attempts int }
type Candidate struct { Agent, Intent, Content string }
type AgentMergeRecord struct { Agent, Branch, Intent string
                               AutoMergedFiles, ConflictedFiles []string
                               TestResult string; Dropped bool; DropReason string }
type IncompatibilityRecord struct { Agent, Intent string; WithIntents []string
                                    Reason, File, Detail, Action, Dropped, Note string }
type VerificationRecord struct { Verdict Verdict; Summary string; Rungs []RungResult }
type Trail struct {
    Task, CreatedAt, Baseline, MergedSHA string
    Agents []AgentMergeRecord; Conflicts []ConflictRecord
    Incompatibilities []IncompatibilityRecord; Verdict Verdict
    FinalTest string; FinalVerification *VerificationRecord
    Summary TrailSummary
}

// Events streamed to the dashboard DO (parity with the TS BraidEvent union).
type Event struct { Type string; /* tagged fields */ ... }
type Emitter interface { Emit(Event); Flush() error }  // HTTP POST to the worker, best-effort
```

### `internal/policy` (port of policy.ts) — pure
```go
type Context struct { Agent AgentInput; IntegratedAgents []AgentInput
                      File, Reason, LastFailure string }
type Decision struct { Action string /* "escalate"|"drop" */; Drop, Note string }
type Policy interface { Name() string; Decide(Context) Decision }
type EscalatePolicy struct{}               // default: halt, record both, drop nothing
type PickWinnerPolicy struct{ Order []string } // drop the loser by priority, continue
```

### `internal/merge/conflict.go` (port of conflict.ts)
- `ParseConflicts(content string) []ConflictHunk` — diff3 marker parser (requires
  `merge.conflictStyle=diff3`): `ours`, `base`, `theirs`, `contextBefore/After`.
- `ApplyResolutions(content string, resolutions []string) string` — splice each resolved region back.
- Pure string logic; direct port.

### `internal/ladder` (port of ladder.ts, with the Rung 2 addition)
```go
type Verifier interface { Verify(ctx context.Context, repoDir string) (models.RungResult, error) }

type Ladder struct { TestFilePatterns []*regexp.Regexp; Build Verifier; Test Verifier }

// Rungs, cheapest-first, short-circuit on first failure:
//   Rung 1  StructuralGuards(repoDir, baseline)   deterministic, pre-exec
//   Rung 2  Build/typecheck (go build / go vet)    catches semantic breaks in Go
//   Rung 3  semantic attribute + repair hook        (merge drives repair; ladder reports)
//   Rung 4  Test execution (GoTestVerifier, `go test -json`)
func (l *Ladder) Run(ctx, repoDir, baseline string) (models.LadderResult, error)

func StructuralGuards(repoDir, baseline string, pats []*regexp.Regexp) models.RungResult
func CountTestFuncs(src string) int   // counts `func Test|Benchmark|Fuzz` + `t.Run(` via go/ast
func IsTestFile(path string) bool     // `_test.go`
```
- `GoTestVerifier` runs `go test -json ./...`, folds results into pass/fail + summary.
- `GoBuildVerifier` runs `go build ./...` (and/or `go vet`); non-zero ⇒ red with the diagnostics as
  Detail (feeds semantic attribution).

### `internal/semantic` (port of semantic.ts → pure Go, pluggable)
```go
type Analyzer interface { Lang() string; Matches(path string) bool
                          Exports(src string) ExportSet; Imports(src string) []ImportRef }
type GoAnalyzer struct{} // go/parser + go/ast: exported idents; imports + package-qualified selectors

type BrokenRef struct { FromFile, Name, Pkg, TargetHint string; Kind string }

// Detection: prefer build diagnostics ("undefined: a.Foo") cross-referenced with the symbol graph;
// fall back to an AST pass for attribution. Returns sorted (deterministic).
func SemanticConflicts(repoDir string, buildDiag string) ([]BrokenRef, error)

// Repair: unambiguous rename ⇒ 1 removed + 1 added export in the target package.
func InferRename(baseSrc, nowSrc, missing string) (newName string, ok bool)
func RepairRename(repoDir string, ref BrokenRef, newName string) (bool, error) // rewrite a.Old→a.New
```
- Registry keyed by extension; ship `GoAnalyzer` only. `tsAnalyzer` (tree-sitter) is a future add behind
  the same interface → keeps C22 open without cgo today.

### `internal/resolver` (port of resolver.ts)
```go
type ResolveInput struct { Path string; Hunk ConflictHunk; OursIntent, TheirsIntent string
                           Attempt int; LastFailure string }
type ResolveOutput struct { Content, Rationale, Strategy string } // strategy: "union"|"llm"
type Resolver interface { Name() string; Resolve(ctx, ResolveInput) (ResolveOutput, error) }

type UnionResolver struct{}                         // deterministic 3-way union (pure)
type WorkersAIResolver struct{ Endpoint string }     // POST {worker}/api/resolve (net/http)
type ClaudeResolver struct{ APIKey, Model, Endpoint string }
type HybridResolver struct{ Union *UnionResolver; LLM OptionalResolver } // union → LLM on retry
func AutoLLMResolver() OptionalResolver               // Claude key? else Workers AI? else nil
```

### `internal/merge/integrate.go` (port of integrate.ts — the loop)
```go
type AgentInput struct { ID, Branch, Intent string }
type Options struct { RepoDir, Baseline, Task string; Agents []AgentInput
                      Resolver resolver.Resolver; Ladder *ladder.Ladder
                      Policy policy.Policy; MaxRetries int
                      Log func(string); Emit func(models.Event) }
type Result struct { MergedSHA string; Trail models.Trail; Green bool; Verdict models.Verdict }

func Integrate(ctx context.Context, o Options) (Result, error)
```
Behaviour parity with the current TS engine: per-agent `git merge`; clean path vs conflict path;
resolve-with-retries (returns resolved/unresolved, no panic); `verifyWithSemanticRepair` (clean merge
failing only the semantic/build rung → `RepairRename` loop, bounded, re-verify); incompatibility →
`abortOrReset` + `policy.Decide` (escalate default / drop) with both candidates recorded; trail written.

---

## 4. Test parity — re-create all 24 TS tests as Go tests

| TS test | Go home | Notes |
|---|---|---|
| `conflict.test.ts` (3) | `merge/conflict_test.go` | diff3 parse/apply |
| `ladder.test.ts` (10) | `ladder/*_test.go` | `CountTestFuncs` (Go `func Test`); guards; build+semantic short-circuit |
| `semantic.test.ts` (7) | `semantic/*_test.go` | exports/imports via `go/ast`; undefined selector; rename infer |
| `semantic-repair.test.ts` (1) | `merge/semantic_repair_test.go` | Go rename → selector-rewrite → green |
| `integrate.test.ts` (1) | `merge/integrate_test.go` | 3 Go agents compose green through the ladder |
| `incompatible.test.ts` (2) | `merge/incompatible_test.go` | escalate / pick-winner |

Target: **≥24 Go tests green** before deleting the old TS integrator.

---

## 5. Sequencing (small, green, committed sub-chunks)

1. `pkg/models` + `internal/policy` (+ tests) — pure, no deps.
2. `internal/merge/conflict.go` (diff3 parse/apply) (+ tests).
3. `internal/ladder`: `Verdict`, `StructuralGuards`, `CountTestFuncs`, `Verifier` seam,
   `GoBuildVerifier` (Rung 2) + `GoTestVerifier` (Rung 4, `go test -json`) (+ tests).
4. `internal/semantic`: `GoAnalyzer`, `SemanticConflicts` (build-diag + AST), `InferRename`,
   `RepairRename` (+ tests). Sort all outputs.
5. `internal/resolver`: union + hybrid + HTTP LLM (+ tests).
6. Go `seed` fixtures (temp-dir Go module) + `internal/merge/integrate.go` with the semantic-repair
   loop + end-to-end + incompatibility tests → **24-test parity**. Add `models.Event` + a best-effort
   HTTP `Emitter`.
7. `cmd/braid`: local demo subcommand (Go equivalent of `demo/run.ts`); prints verdict + dashboard URL.

Then (separate phases, own plans): migrate `pkg/verify` (Keploy — sever Mongo `ReportDB`/zap/ai-service),
pull coverage forward to replace the static counter; migrate `pkg/provenance` (Entire); rebuild the thin
TS edge against the Go core; delete old root TS.

---

## 6. Open questions carried into the next session

1. **Semantic auto-repair in Go = selector rewrite** (`a.Old`→`a.New`). Proceed, or detection-only
   (fail→escalate) until multi-language/tree-sitter lands? (Plan assumes: proceed with rewrite.)
2. **Go target fixture shape:** a small Go `net/http` service mirroring the Hono `getUser` conflict.
   (Plan assumes: yes.)
3. **CI** is Go-only; the frozen `hackathon/` TS is intentionally not CI'd. (Confirm acceptable.)
4. **"All code in this repo"** read as: our own code (Braid/Keploy/Entire) lives here; ordinary `go.mod`
   libraries are still allowed. Keploy's migration will add standard Go deps (zap, gjson, …). (Confirm.)

---

## 7. Invariants to preserve through the whole migration

- `hackathon/` is frozen and runnable; never modified.
- Every sub-chunk leaves `go test ./...` green; commit + push after each (per user preference).
- The old TS integrator stays working until Go hits 24-test parity, then is removed (snapshot preserves it).
- Behaviour parity first, improvements (Keploy coverage, tree-sitter multi-lang, Entire provenance) second.
- Verdicts stay structured (`green/red/inconclusive/incompatible`); tests remain immutable merge inputs.

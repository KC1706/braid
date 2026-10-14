# Status — Braid (Cloudflare Git-for-Agents Hackathon)

> Cross-session memory. Update this at the end of every work session: what changed, what's next,
> what's blocked. Newest entry on top. See `plan.md` for the full plan.

**Deadline:** 2026-10-14 · **Today:** 2026-10-14 · **Phase:** PRODUCTIONIZING THE GO VERSION — GO_PRODUCTION_PLAN **Phase 1 COMPLETE** + **Phase 2a + 2b + 2c + 2d + 2e done** (resolution guards + contract-first + metamorphic + determinism + parallel tree merge); next = the Phase 2 exit-gate writeup, then Phase 3 (hackathon submission on hold — user pivot)

## ✅ 2026-10-14 — Phase 2b/2c done: rung-0 contract-first + rung-5 metamorphic (`internal/contract`, wired into the engine)
- New **`internal/contract`** (pure; `determinism` + stdlib only, no git/I/O — the engine owns checkouts):
  - **Rung 0 (contract-first, `contract.go`):** `Check{Name,Package,Test}` + `Contract{Agent,Intent,Checks}`.
    `Contract.Hash()` = order-independent content hash over the canonicalized checks (reorder → same hash;
    change WHAT is asserted → different hash). `Freeze(c, at)` → `Frozen{Contract,SpecHash,FrozenAt}`;
    `Frozen.Verify()` returns `ErrSpecTampered` if the spec was edited after freezing (the "spec frozen +
    content-hashed BEFORE code" guarantee). `Contract.MissingTests(testSrcs)` = static "test exists before
    code" check (regex over `func Test…`, mirrors `ladder.CountTestFuncs`).
  - **Rung 5 (metamorphic, `metamorphic.go`):** `CheckRunner` seam + `TakeSnapshot` (run a contract's checks
    on a tree) + pure `diffSnapshots` + `CheckPreservation`. The relation: a check that **passed on an
    agent's own fork** must still RUN AND PASS on the merged tree — else a `Violation` (`regressed` |
    `did-not-run`). A fork-failing check imposes no obligation. `GoTestRunner` implements `CheckRunner` over
    the **same `exec.Executor` seam** (`go test -run ^T$ -count=1 -json pkg`), so it runs in the sandbox too.
- **Wired into the engine (`merge.Integrate`):** `Options.Contracts []contract.Contract` + optional
  `Options.MetamorphicRunner` (nil → local `GoTestRunner`). After a build/test-green final verify, Rung 5
  snapshots each INTEGRATED agent's contract on its own fork, restores the integration branch, then
  `CheckPreservation` on the merged tree. A violation flips the run to **red** and is recorded in the trail
  (`models.MetamorphicRecord` + `ContractViolation`, new `Trail.Metamorphic`). Empty `Contracts` → rung
  skipped (production/default path unchanged). Emits `verify(metamorphic)`.
- Tests: `internal/contract` (hash order-independence + tamper detection + MissingTests; `CheckPreservation`
  both-survive / dropped+regressed / fork-failure-ignored; `GoTestRunner` pass/fail/did-not-run json folding
  via a canned exec). **Real git+go fixtures in `internal/merge/metamorphic_test.go`** with the REAL
  `GoTestRunner`: (1) validation + rate-limit each green on their fork → union merge satisfies BOTH
  contracts (0 violations = the Phase-2 exit-gate "both features survived" case); (2) a **pick-winner merge
  that drops rate-limiting builds CLEAN yet Rung 5 catches it** where the build rung (Rung 2) cannot; plus
  engine-level green-path (Checked=2, stays green) and red-path (injected regression flips a build+test-green
  merge to red). **Full suite + vet green; `demo` verdict identical (3 merged · 1 conflict · 0 LLM).**
- **NEXT = Phase 2 exit-gate writeup, then Phase 3.** The gate's three legs are now all met in-tree:
  conflict corpus incl. a *semantic no-textual-conflict* case (rung-2 build catches Go cross-pkg breakage;
  rung-5 catches behavior loss on a clean build — the stronger semantic case); region-confinement + AST
  bounds rejection = 2a; reproducibility twice across N agents = 2d/2e. Carry-overs below still open.

## ✅ 2026-10-14 — Phase 2e done: parallel tree merge (`internal/treemerge` + `merge.IntegrateTree`)
- New **`internal/treemerge`** (pure, no git/I/O) — plans the fold SHAPE: `Plan(n)` builds a balanced
  pairwise reduction tree over n canonically-ordered leaves (pair adjacent nodes level by level, carry an
  odd node up), depth **ceil(log2 n)** vs the linear fold's O(n). `Node` + `IsLeaf/Depth/Leaves`. Leaf
  order preserved (determinism). For n=3 the tree is `merge(merge(0,1),2)` — same associativity as linear.
- New **`merge.IntegrateTree`** (sibling entry point; the proven linear `Integrate` is untouched) — folds
  the agents through the plan: each internal node independently merges two sibling subtrees on a fresh
  `braid/node-k` branch (both descend from baseline → diff3 gives a clean 3-way base), resolving true
  conflicts with the resolver + the Phase-2a guards, verified by a **build-level** node ladder
  (`{Build: o.Ladder.Build}` → structural guards + build only). The **FULL** ladder (incl. the test suite)
  runs **once at the ROOT** (2e's defining property). Agents are canonically ordered first, so with a
  caching resolver the whole tree merge is reproducible. Unresolvable node → `git merge --abort` + coarse
  escalate (per-agent policy attribution over a tree is a later refinement).
  - Refactor: `resolveFileWithRetries` now takes the ladder to verify against (`resolveArgs.lad`, nil →
    `o.Ladder`); the linear caller passes nil (unchanged behavior), the tree passes the node ladder.
- Tests: `internal/treemerge` (depth=ceil(log2 n) for n=1..17; every agent covered once; ascending leaf
  order; n=3 linear-shape) + `internal/merge/integrate_tree_test.go`: 3-agent fixture composes green (1
  union conflict); **tree fold ≡ linear fold** (byte-identical source-tree fingerprint for n=3); 4-agent
  balanced depth-2 tree (conflict surfaces at the root) is green AND reproducible across a repeat run and a
  permuted input order. **Full suite + vet green; `demo` verdict identical (3 merged · 1 conflict · 0 LLM).**
- **NOTE:** `IntegrateTree` is built and tested but not yet wired into `run.Service` (the linear path is
  still the default production entry). Wiring it behind a `RunSpec` flag + attributing per-agent
  incompatibility over the tree is a follow-up.
- **NEXT = Phase 2b/2c (rungs 0 & 5), ship minimal first:** rung 0 = each subtask carries a
  machine-readable acceptance spec, frozen + content-hashed before code (`internal/contract`); rung 5 =
  metamorphic — golden snapshots of each agent's observable behavior on its own fork, assert the merged
  system still satisfies EVERY agent's contract. Then meet the **Phase 2 exit gate** (conflict corpus incl.
  a semantic no-textual-conflict case; region-confinement + AST-bounds rejection [done 2a]; reproducibility
  [done 2d/2e]). See `GO_PRODUCTION_PLAN.md` §3 Phase 2 + §4.

## ✅ 2026-10-13 — Phase 2d done: determinism (`internal/determinism` + resolver + merge)
- New **`internal/determinism`** package — the reproducibility primitives:
  - **`ContentHash(parts…)`** — hex SHA-256 over length-prefixed parts (so the part boundary is
    unambiguous). The one hashing primitive agent-order, the resolution cache and the fingerprint share.
  - **`CanonicalOrder[T](items, key)`** — generic, permutation-invariant ordering: ascending by
    `ContentHash(key(item))` with the raw key as tiebreak. Does not mutate its input.
  - **`TreeHash(repoDir, ref)`** = `git rev-parse ref^{tree}` (content-addressed, metadata-free), and
    **`FingerprintTree(repoDir, ref, excludePrefixes…)`** — hashes `git ls-tree -r` blob entries
    excluding a prefix (used to exclude `.braid/trail.json`, which embeds timestamps and isn't part of
    "the merge result").
- **`resolver.CachingResolver`** — memoizes any inner resolver by
  `ContentHash(path+base+ours+theirs+ctxBefore+ctxAfter+oursIntent+theirsIntent+lastFailure+modelDigest+promptVer)`.
  Identical conflicts under the same model+prompt → identical bytes within AND across runs (the
  determinism the LLM path lacks; free for the deterministic union). `Key()` + `ModelDigestOf()` exported.
  Pinned constants: `claudeDefaultModel`, `workersAIModel`, `PromptVersion="v1"`.
- **temp-0 + model digest in the LLM resolvers:** `temperature:0` added to both the Claude and Workers AI
  request bodies; `ClaudeResolver.ModelDigest()` / `WorkersAIResolver.ModelDigest()` pin the model id into
  the cache key (a model swap invalidates the cache).
- **Canonical agent order wired in:** new `merge.Options.CanonicalOrder bool` (default OFF — the low-level
  order-dependent merge tests, e.g. pick-winner priority, keep it off). When ON, `Integrate` reorders
  agents by `canonicalAgentOrder` = content hash of each branch's `git diff baseline..branch` (+ID/Branch/
  Intent tiebreak). **`run.Service` sets it ON** and wraps the resolver in `CachingResolver` (new
  `resolverForCached`) — production runs are deterministic.
- Tests: `internal/determinism` (hash stable+unambiguous, CanonicalOrder permutation-invariant +
  non-mutating), `internal/resolver/caching_test.go` (memoizes a counting/nondeterministic inner — 1 call
  for 2 identical resolves; key varies with every keyed field incl. prompt version; digest-of), and the
  **reproducibility gate `internal/merge/reproducible_test.go`**: the 3-agent fixture folded in two
  different agent orders (and a 3rd repeat run) yields the IDENTICAL source-tree fingerprint. **Full suite
  + vet green; `demo` verdict identical (3 merged · 1 conflict via union · 0 LLM).**
- **NEXT = Phase 2e parallel tree merge** (fold N agents as a pairwise tree of depth O(log N), each node an
  independent sandboxed job; full suite always at the root). Then 2b/2c rungs 0 & 5, then the Phase 2 exit
  gate (conflict corpus incl. a semantic no-textual-conflict case + the reproducibility CI already proven
  here). See `GO_PRODUCTION_PLAN.md` §3 Phase 2 + §4.

## ✅ 2026-10-13 — Phase 2a done: rung-1 resolution guards (`internal/ladder`)
- New **`internal/ladder/resolution_guard.go`** — three guards run on whatever a resolver (union OR LLM)
  proposes, BEFORE it is written or the tests run (cheaper than build/test; a bad resolution is fed back
  → retry → escalate, never trusted):
  - **`ConfineToConflictRegions(original, resolved)`** (2a-i): the applied file must edit NOTHING outside
    the conflict regions it was handed. Splits `original` into its non-conflict "spine" (n+1 segments for
    n regions, via the diff3 markers) and anchors each segment in `resolved` — spine[0] an exact prefix,
    spine[last] an exact suffix, middles in order. Any changed/deleted surrounding line breaks an anchor
    → reject. Marker regexes are duplicated in-package (no merge→ladder cycle).
  - **`CheckResolutionBounds(hunk, resolution)`** (C14): per-hunk size budget
    `4*(ours+base+theirs)+50` lines, and rejects a replacement that reintroduces any conflict marker
    (can't smuggle a fresh region past the splice).
  - **`ParsesAsGo(path, src)`** (C14): a resolved `.go` file must still `go/parser`-parse (full file);
    non-Go paths skip. Fails fast on LLM garbage before the build rung.
  - **`GuardResolution(...)`** ties all three together — the one call the merge engine makes.
- **Wired into `internal/merge/integrate.go`** `resolveFileWithRetries`: after `ApplyResolutions`, run
  `ladder.GuardResolution`; a violation sets `lastFailure` + logs `⚠ rejected by resolution guard` and
  `continue`s (treated like a red verdict — retry with the reason fed back, eventually → incompatibility
  policy). The union/happy path is unaffected (union output is in-region, in-bounds, parseable).
- Tests: `internal/ladder/resolution_guard_test.go` — confinement accepts in-region, rejects edits
  before/after/between regions + surrounding-line deletion + edits to a no-conflict file + the
  two-region middle-spine case; bounds accepts union-sized, rejects smuggled-marker + ballooned; parse
  accepts valid Go, rejects truncated, skips non-Go; `GuardResolution` end-to-end (clean pass /
  out-of-region fail / non-parsing fail). **Full suite + vet green; `demo` verdict identical (3 merged ·
  1 conflict via union · 0 LLM).**
- **Still open in 2a (deferred, as planned):** real coverage non-regression via Keploy (the static
  `CountTestFuncs` still can't see `t.Skip()` weakening — known gap).
- **NEXT = Phase 2d determinism** (`internal/determinism` + `internal/resolver`): canonical content-hash
  agent order; temp-0 + pinned model digest in the LLM resolvers; resolution cache keyed by
  `hash(base+ours+theirs+context+model-digest+prompt-ver)`; reproducibility test (identical inputs →
  identical output hash, twice). Then 2e parallel tree merge, 2b/2c rungs 0 & 5. See `GO_PRODUCTION_PLAN.md`
  §3 Phase 2 + §4.

## ✅ 2026-10-13 — Phase 1.4 done → PHASE 1 COMPLETE (commit `1b38a2c`)
- Built **`internal/event.HTTPEmitter`** (implements `models.Emitter`): ordered, non-blocking,
  seq-numbered, best-effort POSTs of `models.Event` to `{worker}/runs/{id}/event` — the Go counterpart of
  the TS BraidEmitter. Single drain goroutine = in-order delivery; `Emit` never blocks (full queue drops,
  seq gap is the signal); `Flush` closes + waits (idempotent). Added `models.Event.Seq` (per-run
  monotonic, for consumer gap-detection/replay, C10).
- Wired via the `Service.Emitter` seam: `RunSpec.DashboardURL` → per-run `HTTPEmitter` (per-request
  streaming, correct under concurrent `serve`); `cmd/braid demo` passes `BRAID_WORKER_URL` through.
- Tests: event (order+seq, best-effort-on-unreachable, blank-URL no-op, dashboard URL, idempotent Flush)
  + a run-level integration streaming a real run to a stand-in DO. Full suite + vet green.
- **Gap noted (not a bug):** the Go engine emits `merge:start`/`verify(final)`/… but NOT TS-style
  `run:init`/`run:done`. The existing TS worker `reduce()` keys off those, so the Go stream won't fully
  drive the *old* dashboard. Aligning the event vocabulary with the reducer is a **Phase-5** (console)
  task, deliberately deferred.
- **PHASE 1 COMPLETE (1a Executor seam · 1b SandboxExecutor · 1c RunService+serve · 1d HTTPEmitter).**
  Gate met on the dev backend: engine runs as a service, build+test execute inside the sandbox, live
  events stream; red-team **no-creds + egress-deny proven** (Seatbelt). Still 🟠 and re-run when the
  **Linux container / CF Sandbox backend** lands: host-fs confinement + CPU/mem/pids/disk caps +
  content-addressed bundle input (needs cgroups; this box is darwin).
- **NEXT = Phase 2 (Correctness core):** rung-1 hardening (confine resolver edits to the conflict region;
  AST bounds on LLM output; real coverage via Keploy — the delete/weaken-test block is ALREADY live),
  rung-0 contract-first, rung-5 metamorphic, determinism (canonical order + temp-0 + pinned digest +
  resolution cache), parallel tree merge. Gate = conflict corpus merges correctly + reproducibility CI
  (identical inputs → identical output hash, twice). Could ALSO slot the **container backend** chunk here
  to close the Phase-1 fs/caps half before Phase 3's prod claims.

## 🧩 2026-10-13 — Phase 1.3 done: RunService + `braid serve` (commit `08e98f5`)
- Built **`internal/run`**: `Service.Run(ctx, RunSpec) (RunResult, error)` — the one service layer around
  the engine that both the CLI and the HTTP server go through (and the Worker will call later). Maps a
  declarative `RunSpec` → `merge.Options`: resolver selection (`union`/`auto`/`hybrid`/`workersai`/
  `claude`), executor selection (local vs `SandboxExecutor` via the `Sandbox` flag), ladder wiring, and a
  best-effort `models.Emitter` (Flush at end). `RunResult` = verdict/green/mergedSHA/summary/trail.
- Built **`cmd/braid serve`**: `POST /run` (RunSpec JSON → RunResult JSON) + `GET /healthz`; handler is
  `serveMux(svc)` (extracted for tests). Live smoke verified (healthz ok, /run validates → 422).
- Reimplemented **`cmd/braid demo`** on top of `run.Service` (single code path) + a `--sandbox` flag.
- **Fix found by dogfooding:** running the demo sandboxed went `incompatible` because the Seatbelt profile
  denied ALL network, breaking the demo's `httptest` (loopback) suite. Corrected to deny **external**
  egress but **allow loopback** — real test suites need localhost; exfiltration/remote-reach is what must
  be cut. `demo --sandbox` now GREEN, identical to local (3 merged · 1 conflict · 0 LLM). Added a
  loopback-allowed + external-denied regression pair.
- Tests green across the board incl. new `internal/run` (local+sandbox end-to-end, validation, resolver
  select, emitter) and `cmd/braid` serve (healthz/bad-body/422/full run over HTTP).
- **NEXT = Phase 1 sub-chunk 4 (last of Phase 1):** `internal/event.HTTPEmitter` — best-effort,
  seq-numbered, non-blocking POST of `models.Event`s to the Orchestrator DO `/runs/:id/event` (wire via
  the `Service.Emitter` seam that's already plumbed), with `Flush()`. Then meet the Phase 1 exit gate
  writeup (env/egress red-team already proven; fs/caps deferred to the container backend).

## 🔒 2026-10-13 — Phase 1.2 done: SandboxExecutor (no-creds + egress-deny) (commit `50910ea`)
- Built **`exec.SandboxExecutor`** (implements `exec.Executor` by composing an inner executor under a
  `SandboxBackend` with a credential-free env — the compute-zone half of the two-zone invariant):
  - `Policy{EnvAllowlist, ExtraEnv, DenyEgress, Limits}` + `DefaultPolicy` (minimal non-secret env,
    `GOPROXY=off`, network denied, 5m wall). `SanitizeEnv` strips the host env to the allowlist so **no
    Artifacts token / API key reaches untrusted code**. Added `Request.EnvOverride` (exact env, nothing
    inherited); `LocalExecutor` honors it.
  - Backends behind `SandboxBackend`: **`SeatbeltBackend`** (macOS `sandbox-exec`, denies all network —
    the working dev isolation on THIS box, since it's darwin not Linux/gVisor) + `DirectBackend`
    (env-strip only, honest fallback). `autoBackend` prefers Seatbelt on darwin.
- **Proven on this host:** creds stripped from the process env; egress denied under the sandbox (tested
  vs a live http-200 baseline); the **ladder's real build+test rungs run GREEN inside the sandbox** and a
  clean-but-broken merge still fails the build rung. Full suite + vet green.
- **Still 🟠 (backend-provided, needs cgroups → Linux container / CF Sandbox SDK, slots behind
  `SandboxBackend` with no rung change):** host-fs confinement + CPU/mem/pids/disk caps, and
  content-addressed tree-bundle input (today the sandbox runs over `Dir`). Phase 1 gate is met for
  1a–1d + env/egress red-team on the dev box; the fs/caps half re-runs when the container backend lands.
- **NEXT = Phase 1 sub-chunk 3:** `internal/run.RunService` — `Run(ctx, RunSpec) (RunResult, error)`
  building `merge.Options` (resolver, ladder w/ chosen Executor, policy, retries) + wiring an `Emitter`;
  `cmd/braid serve` over HTTP; reimplement `cmd/braid demo` on top of it (single code path). Then 1.4
  `internal/event.HTTPEmitter` → Orchestrator DO.

## 🔨 2026-10-13 — Phase 1.1 done: Executor seam extracted (commit `75e8470`)
- Built **`internal/exec`** (new pkg): `Executor` interface + `LocalExecutor` (host-process, today's exact
  behavior) + `Request`/`Result`/`Limits`. `Result` keeps **stdout/stderr separate** (the `go test -json`
  rung parses stdout, reads "could not run" from stderr), with a `MergeOutput` mode for the build rung's
  interleaved diagnostics. `Limits.Wall` honored locally via the context; CPU/Mem/Pids/Disk are for the
  sandbox impl.
- Refactored `internal/ladder/verify.go`: `GoBuildVerifier`/`GoTestVerifier` (and `runGo`) now call an
  **injected `Executor`** instead of `os/exec` directly. `Exec` field is nil-defaulted to `LocalExecutor`,
  so every caller (`cmd/braid`, demo, tests) is unchanged. **Pure seam extraction — no behavior change:**
  full suite green (count=1), demo verdict identical (3 merged · 4 auto-merged · 1 conflict · 0 LLM).
  Added `internal/exec/exec_test.go` (stream split, exit codes, merge mode, env, start-failure, wall limit).
- **NEXT = Phase 1 sub-chunk 2:** `SandboxExecutor` implementing `exec.Executor` — run build+test with
  **no creds in the env**, default-deny egress, hard CPU/mem/pids/disk/wall caps, ephemeral per job.
  Dev impl = gVisor/container `LocalSandboxExecutor` (so the Phase 1 gate is testable without cloud);
  CF Sandbox SDK for prod. Then 1.3 `internal/run.RunService` + `cmd/braid serve`, 1.4 `internal/event.HTTPEmitter`.

## 🗺️ 2026-10-13 — GO_PRODUCTION_PLAN.md drafted (next: execute Phase 1)
- Wrote **`GO_PRODUCTION_PLAN.md`** (repo root, commit `1c7e5d0`): the execution plan that turns the
  runnable Go engine into the production system `ARCHITECTURE_PRODUCTION.md` describes. Mirrors
  `GO_MIGRATION_PLAN.md` style — phased **P1–P6, each with a hard exit gate**, deliverables named to the
  Go package. Grounded in the live code + `PRODUCTION_PLAN.md §4–7`.
- Built on the two seams already in the code: `merge.Options.Emit func(models.Event)` (+ `models.Emitter`)
  and `ladder.Verifier interface` (today the verifiers `exec.CommandContext` in-process in `verify.go`).
- **Phase 1 = START HERE** (engine-as-a-service + real test-execution sandbox):
  1. `internal/exec` — `Executor` seam; `LocalExecutor` lifts today's exec logic out of `ladder/verify.go`
     (pure refactor, 47 tests stay green, no behavior change). 2. `SandboxExecutor` (no creds, egress-deny,
     caps; CF Sandbox for prod, gVisor/container for dev). 3. `internal/run.RunService` + `cmd/braid serve`;
     reimplement `demo` on top of it. 4. `internal/event.HTTPEmitter` → Orchestrator DO via `Options.Emit`.
  - **Phase 1 exit gate:** full run with build+test executed INSIDE the sandbox, live events streaming,
    red-team proves no token in the sandbox env + no network/host-fs escape; `LocalExecutor` keeps
    `go run ./cmd/braid demo` + `go test ./...` green.
- Later phases: P2 correctness core (rungs 0/1/5, determinism, tree merge) · P3 control plane + git-I/O
  zone · P4 consistency & provenance (Base-Coordinator CAS, signed ledger) · P5 console (LAST) + resilience
  + $/run · P6 scale. Each gated. See `GO_PRODUCTION_PLAN.md §3` for the full gate list.
- **NEXT when resuming:** execute Phase 1 sub-chunk 1 — `internal/exec.Executor` + `LocalExecutor`,
  refactor the two ladder verifiers to take it, confirm all tests green, commit+push. Then sub-chunks 2–4.

## 📐 2026-10-13 — Production architecture documented + user pivot
- **User decision:** hackathon submission is ON HOLD ("dashboard not up to submission grade"). Focus =
  build the **Go version to production grade**. Do NOT resume video/submit unless the user says so.
- Confirmed the whole Go module is runnable TODAY: `go build/vet/test ./...` all green (47 test funcs),
  `go run ./cmd/braid demo` → verdict green, 3 agents merged, 1 conflict resolved via union, 0 via LLM.
- Wrote **`ARCHITECTURE_PRODUCTION.md`** (repo root): the production-grade architecture — topology,
  single-run data flow, verification ladder (rungs 0–5), parallel fan-in, full API surface
  (control-plane REST + Go engine interfaces), status matrix, AND a plain-English "new-user journey"
  (§0: upload repo → fork-per-agent → sandboxed edits → fan-in merge → verify ladder → signed green).
  Every component tagged ✅ built-Go / 🟠 planned / 🟡 partial-TS. Grounded in real code + PRODUCTION_PLAN §4–6.
- Also published a **rendered-diagram artifact** (Mermaid) of the same content:
  https://claude.ai/artifact/2MWnrxxUMqxSqqA1ZPNG6w (private to the user's account).
- **NEXT when user resumes:** draft `GO_PRODUCTION_PLAN.md` (phased, hard exit gates, like
  GO_MIGRATION_PLAN) then execute. Recommended Phase 1 = extract `Integrate` into a service with a real
  event stream (the `Emit func(models.Event)` seam already exists in merge.Options) + a sandbox for test
  execution. Dashboard/console comes LAST, against a real API — not first.

---


> **TL;DR for a fresh session:** The core thesis (swarm of agents → test-verified hybrid merge) is
> BUILT and PROVEN end-to-end on real Cloudflare Artifacts, AND it now has a deployed live dashboard
> (orchestrator Durable Object + WebSocket). Days 0/1/2/3 effectively done. What's left is the demo
> video + submit, optionally real-LLM agents and CF plumbing (Sandbox/Queue/Workflow). Nothing left
> is high-risk. A legitimate, visually compelling working submission already exists and is deployed.
> As of 2026-10-12 there is ALSO a full production-grade design in `PRODUCTION_PLAN.md` (deep research
> on Artifacts internals + vs GitHub + scaling, a 22-point critical review C1–C22, and the architecture
> that resolves each). That doc is forward-looking (post-hackathon) — it does NOT change the submission.

## Quick state
- [x] Day 0 — de-risk: Artifacts auth + push — ✅ PROVEN live (Bearer header & basic-auth URL both work)
- [x] Day 1 — swarm: fork N + agents push divergent branches — ✅ PROVEN live via control-plane Worker
- [x] Day 2 — integrator (git merge + conflict resolver + re-test loop) — ✅ DONE, TESTED, run live
- [~] Day 3 — live dashboard + orchestrator DO — ✅ DONE & DEPLOYED; review Workflow still TODO
- [~] Day 4 — README + MIT license done; video + submit still to do

## HOW TO RESUME (run these)

### ▶ GO PRODUCTION (current focus — read the top entry, then do the Phase 2 exit-gate writeup → Phase 3)
- Sanity (all green): `go build ./... && go vet ./... && go test ./...`
- Engine demo (local): `go run ./cmd/braid demo`  → verdict green, 3 merged · 1 conflict · 0 LLM
- Engine demo (sandboxed, no creds + egress-deny): `go run ./cmd/braid demo --sandbox`  (same verdict)
- Engine as a service: `go run ./cmd/braid serve` → `GET /healthz`, `POST /run` (RunSpec JSON → RunResult)
- Stream to the deployed DO: set `BRAID_WORKER_URL=https://braid-worker.kunalchandoa925.workers.dev`
  before `demo`/`serve`; it prints a `…/?run=<id>` URL. (Vocabulary gap: Go emits `merge:start`/
  `verify(final)`, not TS `run:init`/`run:done` — old dashboard reducer won't fully drive; Phase 5 aligns.)
- **DONE so far in Phase 2 (ALL rungs):** 2a resolution guards (`internal/ladder/resolution_guard.go`);
  2b/2c contract-first + metamorphic (`internal/contract` + `merge.Options.Contracts`/`MetamorphicRunner`
  + `Trail.Metamorphic`); 2d determinism (`internal/determinism` + `resolver.CachingResolver` +
  temp-0/digest + `Options.CanonicalOrder`); 2e parallel tree merge (`internal/treemerge` +
  `merge.IntegrateTree`). All green, pushed. Full detail in the dated entries at the TOP of this file.
- **NEXT = Phase 2 exit-gate writeup, then Phase 3.** The gate is met in-tree (see the 2b/2c top entry):
  semantic no-textual-conflict case (rung-2 build + rung-5 metamorphic), region-confinement + AST bounds
  (2a), reproducibility twice across N agents (2d/2e). Remaining work is the writeup/corpus consolidation
  then **Phase 3** (control plane + git-I/O trusted zone — `internal/gitio`, `worker/`, Queues/Workflow).
  See `GO_PRODUCTION_PLAN.md` §3 Phase 3.
- **Carry-over follow-ups (not blockers):** wire `merge.IntegrateTree` into `run.Service` behind a
  `RunSpec` flag + per-agent incompatibility attribution over the tree (2e); surface `Options.Contracts`
  through `run.RunSpec` so the metamorphic rung is reachable from the service/CLI (2c); real coverage
  non-regression via Keploy to close the `t.Skip()` gap in `CountTestFuncs` (2a).

### TS hackathon pipeline (ON HOLD — reference only, not the current focus)
- **Live dashboard:** https://braid-worker.kunalchandoa925.workers.dev/  (money-shot visual)
- Local demo (no cloud) + live dashboard: `npm -w integrator run demo` → prints a
  `…/?run=<id>` URL; open it to watch the swarm stream live (events go to the deployed DO).
  Disable streaming with `BRAID_DASHBOARD=off`.
- Full swarm on REAL Artifacts (also streams to dashboard): `npm -w integrator run swarm:live`
- All tests: `npm test`  (baseline 2 + integrator 4)
- Worker: `cd worker && npx wrangler deploy` → https://braid-worker.kunalchandoa925.workers.dev
- **Test the dashboard:** open the home URL, type a run id (e.g. `test1`) in the run-id box → Watch,
  THEN run `BRAID_RUN_ID=test1 npm -w integrator run demo` (open first so you see it animate; the
  local demo finishes in seconds). `swarm:live` animates longer (real forks/pushes). Snapshot without
  a browser: `curl …/runs/<id>/state`. Local-only: `cd worker && npx wrangler dev` then point the demo
  at it with `BRAID_WORKER_URL=http://localhost:8787`. Working = conn pill green, 3 lanes, agent-2
  conflict→resolved (union tag), trail tiles `3/4/2/0`, green banner.

## KEY FACTS (don't re-derive)
- **GitHub:** `KC1706/braid` (PRIVATE). Pushed through commit `be13ea2` (dashboard + DO). User WANTS frequent commits/pushes
  (for their contribution graph) — commit+push after each meaningful chunk. Attribution line in use:
  `Co-Authored-By: Claude Opus 4.8 <noreply@anthropic.com>`.
- **Account:** Workers PAID (user upgraded). Account ID `06bff0b64288392a55e653c32310fc63`.
  `wrangler` logged in as kunalchandoa925@gmail.com. Artifacts namespace = `braid`.
- **Artifacts remote URL:** `https://<ACCOUNT_ID>.artifacts.cloudflare.net/git/<ns>/<repo>.git`
- **Artifacts git auth:** token field is **`plaintext`** (format `art_v2_x_<hex>?expires=<unix>`).
  Basic-auth URL `https://x:<secret>@<host>/...` (secret = token minus `?expires=`) OR
  `git -c http.extraHeader="Authorization: Bearer <full-token>"`. `write` scope needed to push.
- **Binding API:** `env.ARTIFACTS.create/get/list/delete`; handle `.fork(name,{defaultBranchOnly})`,
  `.createToken(scope,ttl)->{plaintext,expiresAt}`, `.log`, `.readFile({ref,path})`, `.info`; handle is
  `using`-disposable. wrangler.jsonc: `"artifacts":[{"binding":"ARTIFACTS","namespace":"braid"}]` + `nodejs_compat`.

## IMPORTANT MENTAL MODEL — what the "agents" currently are
- The 3 "agents" in the demo are **SCRIPTED, not LLMs.** Each applies a hardcoded change from
  `AGENT_EDITS` in `integrator/demo/seed.ts` (agent-1 validation, agent-2 ratelimit — both edit
  `getUser()` → guaranteed conflict; agent-3 docs). `swarm-live.ts` just does clone→write files→
  commit→push per fork. This is deliberate: keeps the demo deterministic and the guaranteed conflict.
- The ONLY live LLM in the pipeline today is **Workers AI**, used for **conflict RESOLUTION** (merge
  engine retry path), NOT for writing the agents' code.
- The novel/hard part = the INTEGRATOR (merge + re-test). Agent codegen is the easy, swappable part.
- To make agents "real": replace the `AGENT_EDITS[...]` file lookup with an LLM call (Workers AI
  `/api/resolve`-style endpoint, or Claude) given the fork's code + intent → returns new file contents.
  Everything downstream is unchanged. The `agent/` package is still an empty placeholder.

## Locked decisions
- Concept A: swarm + hybrid merge (compose, don't pick a winner).
- Resolver is pluggable: `UnionResolver` (deterministic, default) / `ClaudeResolver` (AI Gateway or direct
  Anthropic) / `HybridResolver` (union first, Claude on retry). Verification = REAL tests (not an AI judge).
- Name = **Braid**.

## Open unknowns / NOT yet done
1. Sandbox SDK: run the integrator (clone+merge+npm test) inside a CF Sandbox. Pattern known
   (git-repo-per-sandbox template; auth via `https://x:<secret>@host`). Our integrator is plain Node → should
   move in nearly unchanged. NOT yet built.
2. Agent compute inside Workers via isomorphic-git (needs a MemoryFS class, see docs example). Currently agents
   run from the local swarm-live.ts script (a faithful stand-in). NOT yet relocated.
3. Event subscription `cf.artifacts.repo.pushed` → Queue → review Workflow. NOT yet built.
4. Orchestrator Durable Object (task/agent state + WebSocket) + dashboard. ✅ BUILT & DEPLOYED
   (2026-10-10). DO `Orchestrator` in `worker/src/index.ts` (one instance per run id via
   `idFromName`); ingests structured events (`POST /runs/:id/event`), reduces them to one live
   state snapshot (persisted, survives hibernation), fans out over WebSocket (`/runs/:id/ws`).
   Dashboard = zero-build static page `dashboard/public/index.html`, served by the worker via
   static assets (`ASSETS` binding). Both demos stream to it via `integrator/src/events.ts`
   (`BraidEmitter`) + an optional `onEvent` hook in `integrate()`. Verified end-to-end live
   (WS snapshot replay to late joiners works). NOTE: it's vanilla HTML/JS, not React (simpler to
   deploy, no build step) — same money-shot visual. STILL TODO: push→Queue→review Workflow (#3).
5. LLM conflict resolution: **Workers AI path ADDED & verified live** (no Anthropic key needed) —
   worker `POST /api/resolve` runs `@cf/meta/llama-3.3-70b-instruct-fp8-fast`; integrator has
   `WorkersAIResolver` + `autoLlmResolver()` (Claude if ANTHROPIC_API_KEY, else Workers AI if
   BRAID_WORKER_URL, else union-only). Hybrid uses union first, LLM only on retry. `.dev.vars`/
   `ANTHROPIC_API_KEY` still optional (ClaudeResolver path untested live).
6. `VERIFY.md` added — full manual step-by-step to verify the whole flow by hand.

## NEXT STEP (user to choose)
Dashboard + orchestrator DO = ✅ DONE (2026-10-10). Remaining strong options:
1. **Record the demo video + submit** — the pipeline + live dashboard are the money shot; this is
   now a complete, compelling submission. Highest-leverage use of remaining time before 2026-10-14.
2. **Real LLM agents** — make agents actually WRITE their code via Workers AI (new worker endpoint
   `/api/codegen` given fork code + intent → new files), replacing `AGENT_EDITS`. Makes the "swarm of
   real agents" story fully true end-to-end.
3. **CF-native plumbing** — move integrator into a Sandbox; wire push→Queue→review Workflow;
   Workers Builds preview URL. More infra than demo payoff.
Reminder: `.dev.vars` ANTHROPIC_API_KEY optional (Workers AI already covers LLM, no key needed).

---

## 🎉 END-TO-END SWARM ON REAL ARTIFACTS — PROVEN (2026-10-10)
- `integrator/demo/swarm-live.ts` (`npm -w integrator run swarm:live`) runs the WHOLE pipeline on real
  Cloudflare Artifacts: control-plane Worker creates base repo → seed baseline (real git push) → fork ×3
  (real Artifacts forks) → 3 agents push divergent branches to their forks (agent-1 validation & agent-2
  ratelimit both edit getUser → conflict; agent-3 docs → clean) → integrator clones base, fetches all forks,
  merges (git auto + union conflict resolution), re-runs REAL vitest green after each merge, pushes merged
  result back to base/main. Writes `.braid/trail.json`.
- **Independently verified:** fresh `git clone` of the base repo contains merged `users.ts` (BOTH features),
  agent files, trail.json, and full history (3 agent commits → 2 merge commits → trail commit). Tests 2/2.
- Token field from `issue-token`/binding is **`plaintext`** (not `token`); git auth via basic-auth URL
  `https://x:<secret>@host/...` (secret = token minus `?expires=`) OR Bearer `http.extraHeader` — both work.
- This = Day-1 (swarm) + Day-2 (integrator) demonstrated on real infra. Local-driven orchestration is the
  stand-in for Sandbox/agent-Workers (same git+merge+test code will move there unchanged).
- STILL TODO for full "built-the-CF-way" story: move agent compute into Workers (iso-git, MemoryFS) and/or
  integrator into a Sandbox; push events → Queue → review Workflow; orchestrator DO + WS dashboard; Workers
  Builds preview URL. Anthropic key (.dev.vars) still pending → ClaudeResolver runs union-only for now.

## Day-1 progress (verified LIVE 2026-10-10) — Artifacts fully de-risked
- **User upgraded to Workers Paid.** Artifacts access now granted (namespaces list returns []).
- **Live push round-trip PROVEN:** created repo `braid/braid-smoketest`, pushed from local git via
  `http.extraHeader="Authorization: Bearer <token>"`, cloned back, content matched. Risk #1 = done for real.
- **Control-plane Worker DEPLOYED:** `worker/` → https://braid-worker.kunalchandoa925.workers.dev
  - Binding `env.ARTIFACTS` (namespace `braid`), `compatibility_flags: [nodejs_compat]`.
  - `POST /api/provision {forks:N}` → `create()` base repo, `fork()` it N times (defaultBranchOnly),
    `createToken("write",3600)` per fork → returns remotes + plaintext tokens. **Verified: 3 forks + tokens.**
  - **Fork primitive (last Artifacts unknown) confirmed working via the binding.**
- Binding API (from docs, confirmed): `ARTIFACTS.create/get/list/delete`; handle `.fork(name,{defaultBranchOnly})`,
  `.createToken(scope,ttl)->{plaintext,expiresAt}`, `.log`, `.readFile({ref,path})`, `.info`. Handle is `using`-disposable.
- NEXT: orchestrator DO (task/agent state + WS for dashboard) + agent loop (codegen → iso-git push to fork);
  then risk #2 (Sandbox clone+merge+test). Anthropic key (.dev.vars) still pending → agent codegen falls back
  to deterministic canned edits until provided.

## Day-0 findings (verified 2026-10-10)
- **wrangler authed** as kunalchandoa925@gmail.com. **Account ID:** `06bff0b64288392a55e653c32310fc63`.
  OAuth token has `artifacts:write` + `ai:write` scopes.
- **BLOCKER — Artifacts requires the Workers PAID plan.** Account is on Free → every Artifacts API call
  returns `Access denied [code: 10004]` (confirmed on `namespaces list` and `repos create`). Workers Paid
  is $5/mo; Artifacts usage billing starts 2026-10-14 (10k ops + 1GB-mo free tier). Sandboxes/Containers
  and Workers deploy also need Paid. **→ user must upgrade to Workers Paid to run the cloud layer.**
- **Risk #1 (isomorphic-git ↔ Artifacts push auth) — RESOLVED from docs:**
  - Remote URL: `https://<ACCOUNT_ID>.artifacts.cloudflare.net/git/<namespace>/<repo>.git`
  - Auth: `Authorization: Bearer <token>` via `git -c http.extraHeader=...` (or token-secret as basic-auth
    password: `https://x:<secret>@<host>/...`). Token format `art_v1_<40hex>?expires=<unix>`; secret = part
    before `?expires=`. **write** scope required to push; read scope = clone/fetch/pull only.
  - isomorphic-git `onAuth`: use the Bearer header (docs show git CLI; map to http.headers in iso-git).
- **Repo/token provisioning paths:** (a) `wrangler artifacts repos create <name> --namespace <ns>` +
  `... issue-token <repo> --namespace <ns> --scope write --ttl <s> --json`; (b) Worker binding
  `env.ARTIFACTS.create(name)` returns `{name, remote, token}`. Namespaces auto-create on first repo.
- **Binding config (wrangler.jsonc):** `"artifacts": [{ "binding": "ARTIFACTS", "namespace": "default" }]`.
- Still TODO: verify risk #2 (Sandbox clone+merge+test) and event subscriptions — both need Paid plan.
- **Anthropic key still not provided** (.dev.vars absent) — needed for live agent codegen + ClaudeResolver.

## Change log

### 2026-10-12 — Go brain port COMPLETE: `GO_MIGRATION_PLAN.md` §5 steps 1–7 all done
- The whole TS "brain" is ported to Go (`internal/{merge,ladder,resolver,semantic,policy}`,
  `pkg/models`), at behavior parity. Each step its own commit, all pushed, `go test ./...` green
  throughout (47 Go test funcs — finer-grained than the 24 TS tests, same behaviors covered).
  - **§5.1 `pkg/models` + `internal/policy`** — shared types (Verdict/RungResult/LadderResult/Trail/
    Event/Emitter + ConflictHunk; JSON tags mirror TS camelCase so trail.json stays edge/dashboard-
    compatible) + incompatibility policy (EscalatePolicy default / PickWinnerPolicy opt-in).
  - **§5.2 `internal/merge/conflict.go`** — diff3 ParseConflicts + ApplyResolutions.
  - **§5.3 `internal/ladder`** — Verifier seam + Ladder.Run (cheapest-first, short-circuit): Rung 1
    StructuralGuards (C2), Rung 2 GoBuildVerifier `go build/vet` (a clean-but-broken cross-pkg ref is
    a compile error → robustly catches C1), Rung 4 GoTestVerifier `go test -json`. CountTestFuncs is
    regex-based (judges unparseable trees); t.Skip() gap noted (Keploy coverage closes it later).
  - **§5.4 `internal/semantic`** — Analyzer seam (multi-lang-ready, C22); GoAnalyzer (Exports/Imports/
    PackageExports/ImportPathFor via go/ast); SemanticConflicts (attributes from `go build` diagnostics);
    InferRename/InferRenameAcross (unambiguous rename only; refuses deletions); RepairRename (selector
    rewrite a.Old→a.New via go/ast positions + text splice — the Go repair primitive, §1.3).
  - **§5.5 `internal/resolver`** — UnionResolver (deterministic, parity with TS union output) +
    WorkersAIResolver/ClaudeResolver (HTTP) + HybridResolver (union→LLM on retry) + AutoLLMResolver.
  - **§5.6 `internal/merge/integrate.go`** — the loop: per-agent merge, conflict resolve-with-retries,
    verifyWithSemanticRepair (build-only failure → rename-repair → re-verify, bounded), incompatibility
    abort/reset + policy, writes .braid/trail.json. Tests mirror the TS integrate/incompatible/
    semantic-repair suites (3-agent compose-green, clean-but-broken rename→green, escalate+pick-winner).
  - **§5.7 `cmd/braid demo` + `internal/demo`** — seeds a Go net/http /users fixture (3 agents,
    getUser conflict) and integrates; `braid demo` prints verdict + trail summary. Verified: swarm
    composes 3 agents GREEN (agent-2 conflict via union, 2/2 tests pass).
- **NEXT = post-brain phases (own plans):** migrate `pkg/verify` (Keploy — sever Mongo ReportDB/zap/
  ai-service; pull coverage forward to replace the static test counter), migrate `pkg/provenance`
  (Entire), rebuild the thin TS `edge/` against the Go core, then delete the old root TS workspaces
  (`baseline/ integrator/ worker/ dashboard/` — the frozen `hackathon/` snapshot preserves them).
  `hackathon/` stays frozen and must not be modified. See `braid-production-direction` memory.

### 2026-10-12 — DIRECTION CHANGE: production Go rewrite; froze hackathon snapshot
- **Decision (user):** Braid is a **production** build, not just a hackathon demo. The whole thing is
  being **rewritten in Go** (single language) and the user's own **Keploy + Entire source is migrated
  IN-TREE** (all code in THIS repo, nothing referenced externally / no upstream). Keploy → the
  automated **test layer** (`pkg/verify`: coverage, test-gen, replay, contracts). Entire → **provenance
  + agent memory** (`pkg/provenance`: checkpoints-as-git-refs, redaction, attribution). See
  `PRODUCTION_PLAN.md` + memory `braid-production-direction`.
- **Agreed architecture (Go monorepo, module `github.com/KC1706/braid`, Go 1.26):**
  `internal/{merge,ladder,resolver,semantic,policy}` = Braid's brain (port of the TS integrator;
  `semantic` upgrades to **tree-sitter** = multi-language, also fixes C22); `pkg/verify` (from Keploy)
  + `pkg/provenance` (from Entire); `pkg/gitx`, `pkg/models`. The CF edge (Worker + Orchestrator DO +
  Artifacts binding + dashboard) stays a **thin TS shim** in `edge/` — Workers don't run native Go —
  fronting a fat Go core in a Container/Sandbox. `sandbox/` = container image. Keploy's cloud deps
  (Mongo `ReportDB`, zap, ai-service) get severed and replaced with small Braid interfaces.
- **Migration = strangler, not big-bang:** freeze snapshot → Go skeleton+CI → port brain (re-create the
  24 tests in Go) → migrate `pkg/verify` (pull coverage forward to de-risk Mongo-severing) → migrate
  `pkg/provenance` → rebuild thin TS edge → delete old TS. Keep something green at every step.
- **DONE this chunk — froze the hackathon submission** into **`hackathon/`**: a self-contained copy of
  the proven TS demo (baseline + integrator + worker + dashboard + README/VERIFY/plan + SNAPSHOT.md),
  standalone workspace (`cd hackathon && npm install && npm test && npm run demo`). Verified green
  (baseline 2 + integrator 24). **Do not modify `hackathon/`** — the Go rewrite happens at repo root;
  this snapshot stays stable + recordable. `.gitignore` `.tmp/` de-anchored so snapshot test repos
  aren't tracked.
- **NEXT:** stand up the Go module skeleton (`go.mod`, `cmd/braid`, `internal/*`, `pkg/*`) + CI, then
  begin porting the brain (`gitx` → `merge` → `ladder` → `resolver` → `policy` → `semantic`).
- **✅ Go skeleton DONE** (commit `42ff2b5`): module `github.com/KC1706/braid` (Go 1.26), `cmd/braid`,
  `pkg/gitx` ported + tested, CI (`.github/workflows/go.yml`), `ARCHITECTURE.md`.
- **📋 REVIEWED EXECUTION PLAN for the brain port → `GO_MIGRATION_PLAN.md`** (read this to resume).
  Pure-Go semantic (no cgo/tree-sitter). Key reviewer corrections baked in: Go semantic detection folds
  into a **build/typecheck rung (Rung 2)**; Rung 3 becomes attribute+repair; repair = **selector
  rewrite** `a.Old→a.New` (Go has no import alias); static `func Test` counter is weak vs `t.Skip()`
  (real guard = Keploy coverage later); use `go test -json`; **sort all map-derived output** for
  determinism; fixtures in `t.TempDir()`; Go-targets-only for now (multi-lang via `Analyzer` iface +
  Keploy later). **NEXT = `GO_MIGRATION_PLAN.md` §5 step 1** (`pkg/models` + `internal/policy`), then
  down the list to 24-test parity, committing each sub-chunk.

### 2026-10-12 — Phase 1 chunk 4: Rung 3 AUTO-FIX — detect→resolve (fully closes C1)
- Turns Rung 3 from "detect and fail honestly" into "detect and RESOLVE" (the §5.1 intent: "force that
  pair into the resolution path"). Fully deterministic, no LLM, real tests still the final gate.
- **`semantic.ts`:** `inferRename()` — for a dangling import caused by a rename, the new name is
  inferable iff the target module dropped exactly the missing symbol and gained exactly one export
  (unambiguous; returns null otherwise so a genuine deletion is NOT papered over). `aliasImport()` —
  surgically rewrites the importer via the TS AST: `import { getUser }` → `import { fetchUser as getUser }`,
  preserving any existing local alias and leaving every downstream reference untouched.
- **`integrate.ts`:** per-agent verification now goes through `verifyWithSemanticRepair()` — if a clean
  merge fails ONLY the semantic rung, it repairs one dangling reference per round (alias), commits
  `braid: semantic repair …`, and re-runs the full ladder, bounded (≤6 rounds). If nothing can be
  safely inferred it falls through to the incompatibility policy (escalate by default). Emits a `log`
  event per repair so the dashboard shows it.
- **Test:** `integrator/test/semantic-repair.test.ts` — agent-a renames getUser→fetchUser (fixing its
  own caller); agent-b adds a new file still importing the OLD name. The merge is textually CLEAN but
  broken; Braid auto-aliases c.ts and goes **green**, with downstream `getUser("x")` untouched and zero
  incompatibilities. Suite green: integrator **24** + baseline 2; 3-agent happy path still green.
- **C1 is now fully closed** (detect + resolve). Phase-1 remaining: Rung 0 contract-first frozen tests
  (C3); Rung 2 build/typecheck; pluggable toolchain (C22). LLM-backed semantic repair (ambiguous
  renames/true deletions) is a future add — today those correctly escalate.

### 2026-10-12 — Phase 1 chunk 3: incompatibility path (fixes C4 — "union is not a law")
- User decision: build it pluggable, **default = escalate (never silently drops an agent's work)**;
  pick-winner available as an opt-in for full autonomy.
- **New: `integrator/src/policy.ts`** — `IncompatibilityPolicy` interface + `EscalatePolicy` (default,
  halts the run with an `incompatible` verdict and records BOTH candidates) + `PickWinnerPolicy(order)`
  (drops the loser by priority, records the discarded candidate, continues).
- **`integrate.ts` refactored:** the engine no longer THROWS when an agent can't be composed (the old
  behaviour on clean-merge-red or resolver-exhaustion). Instead it detects incompatibility (conflict
  unresolved after `maxRetries`, OR a clean merge that fails the ladder), **git-aborts/resets the merge
  back to the last good state** (`abortOrReset`), applies the policy, and records an
  `IncompatibilityRecord`. `resolveFileWithRetries` now returns `{resolved}` instead of throwing.
  Per-agent conflict provenance is only committed to the trail if that agent fully composes (an aborted
  merge leaves no partial records). `IntegrateResult` gained `verdict`; `Trail` gained `verdict` +
  `incompatibilities[]`; `AgentMergeRecord` gained `dropped`/`dropReason`; new `incompatible` event.
- **Tests:** `integrator/test/incompatible.test.ts` (2) — default policy escalates (verdict
  `incompatible`, green false, both candidates recorded, nothing dropped, NO throw); pick-winner drops
  the loser and continues to green with the dropped agent recorded. Suite green: integrator **23** +
  baseline 2. End-to-end 3-agent happy path still composes green; local demo prints the verdict + any
  incompatibilities and the trail now shows all rungs (structural-guards → semantic-conflicts →
  test-execution).
- **Phase-1 status:** C1 (detect), C2 (immutable tests), C4 (incompatibility path) now have mechanical
  coverage + tests. STILL TODO to fully close the gate: **route** a detected Rung-3 semantic pair back
  into the resolver (auto-fix, not just detect); Rung 0 contract-first frozen tests (C3); Rung 2
  build/typecheck; pluggable toolchain (C22).

### 2026-10-12 — Phase 1 chunk 2: Rung 3 semantic-conflict detection (fixes C1)
- **New: `integrator/src/semantic.ts`** — the semantic-conflict detector (§5.1 Rung 3), using the
  **real TypeScript parser** (typescript 5.9.3, now a declared integrator dep — no fragile regex).
  Builds a symbol graph of the merged working tree: per-module exports (named/default, incl.
  `export *` re-export following) + import bindings. Flags every **intra-repo relative import whose
  named/default binding is absent from the target module's exports** — i.e. a textually-CLEAN merge
  that left a reference wired to nothing (the canonical "fork A renamed/removed a symbol fork B still
  imports"). Conservative by design: bare/node_modules specifiers and namespace imports are ignored,
  so false positives are near-zero.
- **Wired as Rung 3 in the ladder**, *between* structural guards and test execution. It's deterministic
  and cheaper than the suite, so it runs first and catches breakage the tests may not cover — and when
  it fails, **tests never run** (cheapest-first short-circuit). This is the direct **fix for C1**
  ("the clean-merge path is the dangerous one; the engine only handled textual conflicts").
- **Tests:** `integrator/test/semantic.test.ts` (7) — exports/imports extraction, the rename-across-
  forks dangling-import case, missing-default, `export *` following, and no-false-positive on
  bare/namespace imports. Plus a ladder-level test proving a broken merged tree yields verdict `red`
  with the semantic rung deciding and test-execution skipped. Suite green: integrator **21** + baseline 2.
  End-to-end integrate still composes 3 agents green *through* Rung 3 (no false positive on the real
  fixture).
- **Phase-1 exit gate:** both headline items now have mechanical coverage — delete-the-test blocked
  (chunk 1) AND semantic/no-textual conflict detected (chunk 2). STILL TODO to fully close the gate:
  **route** a detected Rung-3 pair back into the resolver (currently detect+fail honestly, not yet
  auto-resolve); the **incompatibility / pick-a-winner path** (C4); Rung 0 contract-first frozen tests
  (C3); Rung 2 build/typecheck; pluggable toolchain (C22).

### 2026-10-12 — PRODUCTION BUILD STARTED — Phase 1 chunk 1: verification ladder + Rung 1 structural guards
- User decided to STOP at demo-polish messaging and build the `PRODUCTION_PLAN.md` for real. No more
  "record the video" recommendations. Implementing the plan phase by phase, committing each chunk.
- **New: `integrator/src/ladder.ts`** — the verification ladder (§5.1). Replaces exit-code-only verify
  with a structured `Verdict` (`green`/`red`/`inconclusive`/`incompatible`) + per-rung results.
  - **Rung 1 structural guards (deterministic, pre-execution)** implemented: compares the frozen
    `baseline` tree against the merged working tree and REJECTS the merge if any test file was deleted
    or had its executable test-case count reduced (per-file or total). `countTestCases()` statically
    counts `it(`/`test(` — `.skip`/`xit`/commented-out do NOT count, so skipping reads as a coverage
    drop. This is Decision 2 ("tests are immutable merge inputs") enforced mechanically → **fixes C2
    ("cheapest way to green is to delete the test") and first line vs C14.**
  - **Rung 4 = the existing real-suite `verify()`**, now called *after* the cheap guards.
  - `runLadder()` runs guards → tests, returns the deciding verdict.
- **Wired through `integrate.ts`:** all three verify call-sites (per-agent, final, resolver-retry loop)
  now go through `runLadder(repoDir, baseline, …)`. So a union/LLM resolution that touches a test file
  is caught and fed back / rejected, not silently accepted.
- **Provenance:** `trail.ts` gained `finalVerification` (verdict + rungs); `events.ts` `verify` event
  gained optional `verdict`/`rungs` (dashboard-backward-compatible).
- **Tests:** `integrator/test/ladder.test.ts` (9) proves delete-the-test and weaken-the-test are both
  rejected, adding tests is allowed, and the static counter ignores skips/comments/method-calls. Full
  suite green: integrator 13 + baseline 2. End-to-end integrate still composes 3 agents green through
  the ladder (happy path unbroken).
- **NEXT (Phase 1 remaining):** Rung 3 semantic-conflict detection (symbol graph: force clean-but-
  semantically-conflicting pairs into the resolve path — fixes C1); the incompatibility / pick-a-winner
  path (`incompatible` verdict + policy — fixes C4); Rung 0 contract-first frozen tests (C3); Rung 2
  build/typecheck; pluggable toolchain (C22). Phase-1 exit gate = semantic-conflict corpus merges
  correctly AND delete-the-test blocked (second half now DONE).

### 2026-10-12 — production-grade plan written (research + critical review + architecture)
- **New doc: `PRODUCTION_PLAN.md`** (repo root). No code changed; this is forward-looking design for
  AFTER the hackathon. The hackathon submission is unaffected.
- Deep research (Cloudflare blogs + independent deep-dives + GitHub DGit/Spokes) into (a) what Artifacts
  really is — DO-per-repo, ~100KB Zig→WASM git engine, SQLite/2MB rows, R2 snapshots, KV tokens, ~128MB
  DO mem → streaming, events→Queue→Workflow, 1GB/repo cap, $0.15/1k ops + $0.50/GB-mo; (b) how it differs
  from GitHub (serverless DO-per-repo vs 3-replica Spokes fleet; machines vs humans); (c) scaling: storage
  scales great, the REAL bottleneck is the merge+verify FAN-IN (a job-queue + sandbox-pool problem).
- Did 3 rounds of senior-eng critical review → distilled to **22 flaws C1–C22** (clean-merge semantic
  breakage, "delete-the-test to go green", green≠correct, serial/order-dependent/non-reproducible fan-in,
  base-write race, self-triggering push→event loop, Orchestrator DO quadratic writes + event-loss
  corruption, open unauthenticated control plane, untrusted-code+token RCE, prompt-injection→RCE, forgeable
  in-repo provenance, SHA-1 anchor, blind-on-correctness, no DR on a beta, no unit economics, JS-only).
- Wrote the architecture that fixes each (traceability matrix C1→fix in the doc): **two foundational
  decisions** (customer = machine; "green" = multi-layer verdict w/ immutable tests); a **verification
  ladder** (contract-first tests → structural guards → build/typecheck → semantic-conflict graph →
  selected tests → contract/metamorphic → incompatibility/pick-a-winner path); **deterministic parallel
  tree-merge** (canonical order, temp-0 + pinned model digest, resolution cache); **two-zone sandbox**
  (untrusted compute w/ no creds+egress-deny vs git-I/O zone holding the token); authenticated control
  plane; **externalized signed provenance ledger** (SHA-256); Base-Coordinator DO (CAS on parent sha) +
  event-loop filter/dedup; DR mirror to R2/2nd remote; correctness observability + rollback + trust tiers;
  unit-cost governor; operator/auditor frontend (Next.js on Pages). 6-phase roadmap, each with a hard gate.
- NOTE: this is a PLAN only (user asked plan-first, no implementation). Natural next engineering step if
  pursued = Phase 1 (correctness core / verification ladder). But for the HACKATHON the next step is still
  record video + submit (below).

### 2026-10-10 — live dashboard + orchestrator Durable Object (the money shot)
- **Worker: `Orchestrator` Durable Object** (`worker/src/index.ts`) — one instance per run id
  (`env.ORCHESTRATOR.idFromName(runId)`). `POST /runs/:id/event` ingests structured events →
  pure `reduce()` folds them into one `RunState` snapshot → persisted to DO storage (survives
  hibernation) → broadcast as `{type:"state",state}` to all WS clients. `GET /runs/:id/ws`
  (hibernation API: `acceptWebSocket`, sends snapshot on connect) · `GET /runs/:id/state` (poll) ·
  `POST /runs/:id/reset`. Service JSON moved from `/` to `/api`.
- **wrangler.jsonc:** added `durable_objects` binding + `migrations` (`new_sqlite_classes`) +
  `assets` binding (`../dashboard/public`). Deployed; version be515bab.
- **Dashboard:** `dashboard/public/index.html` — zero-build static page served by the worker.
  Dark UI: agent swim lanes (spawned→forked→coding→pushed + merge status), live conflict-resolution
  panel (union vs llm tags, attempts, resolving/resolved), provenance trail (4 stat tiles + final
  test + merged sha + merged users.ts), activity log. Reconnecting WS; `?run=<id>` selects the run;
  manual run-id entry if absent. Renders purely from the server snapshot (no client-side reducer).
- **Instrumentation:** `integrator/src/events.ts` (`BraidEmitter` best-effort fire-and-forget POST,
  `BraidEvent` union) + optional `onEvent` hook threaded through `integrate()` (emits merge:start,
  merge:auto, conflict:detected/attempt/resolved, verify). Both `demo/run.ts` (local, mode="local")
  and `demo/swarm-live.ts` (live, mode="live") create a run id, emit run:init→agent:status→…→run:done,
  and print the dashboard URL. Local demo streams to the DEPLOYED DO → dashboard lights up with NO
  local cloud. `BRAID_DASHBOARD=off` disables; `BRAID_RUN_ID` pins the id.
- **Verified live end-to-end:** ran `demo`, polled `/runs/:id/state` (3 agents, getUser conflict
  resolved via union, final GREEN, usersFile present), and a real WS client received the full
  snapshot on connect (late-joiner replay works). Tests still 4/4. Page serves 200 at `/`.

### 2026-10-10 — Workers AI resolver + manual verify guide
- Added Workers AI conflict resolution so NO Anthropic key is needed: worker `POST /api/resolve`
  (binding `env.AI`, model `@cf/meta/llama-3.3-70b-instruct-fp8-fast`); verified live (returned the
  correct `validateId; rateLimit; return` composition).
- integrator: `WorkersAIResolver` (calls `${BRAID_WORKER_URL}/api/resolve`) + `autoLlmResolver()`;
  `HybridResolver` now takes any optional LLM resolver. demos auto-pick Workers AI. Tests still 4/4.
- Wrote `VERIFY.md`: detailed by-hand verification of the entire flow (local engine → control plane →
  Workers AI → full live swarm → independent clone check → dashboard list → cleanup).

### 2026-10-10 — Day-0 de-risk: auth works, blocked on Workers Paid plan
- `wrangler login` done; confirmed Artifacts needs Workers Paid (Free → 10004). Resolved the #1 risk
  (iso-git push auth/URL format) straight from docs. Documented provisioning + binding config above.
- No code change beyond this log; next cloud work gated on the plan upgrade.

### 2026-10-10 — built the merge engine (the thesis), offline-first
- Scaffolded monorepo (npm workspaces): `baseline/ integrator/ agent/ worker/ dashboard/`, `git init`,
  MIT `LICENSE`, root `README.md` with run instructions, `.gitignore`.
- **`baseline/`** — Hono `/users` API (`src/users.ts` holds the `getUser()` conflict locus) + real
  vitest suite (2 tests, green). This is both the fork target and the acceptance gate.
- **`integrator/` — the star, DONE and tested:**
  - `src/git.ts` real-git wrappers; `src/conflict.ts` diff3 hunk parser + splicer;
    `src/resolver.ts` `UnionResolver` (deterministic 3-way union) + `ClaudeResolver` (AI Gateway/Anthropic)
    + `HybridResolver` (union first, Claude on retry); `src/verify.ts` runs the REAL test suite as the
    gate; `src/integrate.ts` iterative `git merge` + conflict resolution + re-test-and-retry + writes
    `.braid/trail.json`; `src/trail.ts` provenance types.
  - `demo/seed.ts` + `demo/run.ts`: `npm run demo` seeds baseline + 3 agent branches (agent-1 validation
    & agent-2 ratelimit both edit `getUser()` → guaranteed conflict; agent-3 docs → clean auto-merge)
    and integrates. **Reproduces the plan's money shot: both features survive, tests green, trail written.**
  - Tests: `npm -w integrator test` → 4 pass incl. a full end-to-end integrate (real git + real vitest gate).
- **Key proof:** the hybrid merge (NEVER-drop thesis) works on real git + real tests with zero cloud.
  Production integrator runs the same git in a Sandbox — identical code path.
- Decided to build cloud-independent core first since env has no Cloudflare/Anthropic creds (see below).
- **Did NOT git-commit** (waiting for user). Repo is `git init`'d on branch `main`.
- **Next (needs creds):** Day 1 swarm — `worker/` DO orchestrator + `env.ARTIFACTS` forks + queue consumer;
  `agent/` Claude-codegen + isomorphic-git push; then Day 3 Workflow + dashboard. To unblock: `wrangler login`,
  set `ANTHROPIC_API_KEY` (+ optional `CF_AI_GATEWAY`), enable Artifacts beta.

### 2026-10-10 — planning
- Researched Artifacts API, limits, events; confirmed no native merge/diff → that gap is the project.
- Researched LLM merge literature → chose hybrid (deterministic git + LLM only for true conflicts),
  real test verification over AI-judge.
- Wrote `plan.md` (full architecture, 4-question rubric mapping, merge worked example, day-by-day).
- Created this `status.md`.
- Checked `~/entire-playground` (user's "similar" dir): near-empty scratch repo, nothing to reuse.
- **Next:** start Day 0 de-risk (isomorphic-git push test). Confirm env checklist items first.

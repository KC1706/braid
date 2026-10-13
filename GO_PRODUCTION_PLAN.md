# Braid — Go Production Plan (brain → product)

> **Companion to** `GO_MIGRATION_PLAN.md` (which ported the TS brain to Go, now complete) and
> `ARCHITECTURE_PRODUCTION.md` (the production target). This is the **execution plan** that turns the
> runnable Go engine into the production system `ARCHITECTURE_PRODUCTION.md` describes — phased, each
> phase with a **hard exit gate** (no advance until met), each deliverable named down to the Go package.
>
> **Legend:** ✅ built today · 🟠 to build · 🟡 partial. Status colors mark implementation state, not
> importance. Hackathon submission is **on hold** (user pivot, 2026-10-13); this plan is the Go product.

---

## 0. Where we start (as of 2026-10-13)

The brain is done and green. `go build/vet/test ./...` pass (47 test funcs); `go run ./cmd/braid demo`
→ verdict green, 3 agents merged, 1 conflict resolved via union, 0 via LLM.

**What exists (the spine):**

| Package | Role | State |
|---|---|---|
| `internal/merge` | `Integrate(ctx, Options) (Result, error)` — the loop: merge · resolve · semantic-repair · trail | ✅ |
| `internal/ladder` | `Verifier` seam + `GoBuildVerifier` (rung 2) · `GoTestVerifier` (rung 4) · `StructuralGuards` (rung 1, run unconditionally by `Ladder.Run`) | ✅ / 🟡 guards |
| `internal/semantic` | clean-merge-but-broken detection + `RepairRename` (rung 3) | ✅ |
| `internal/resolver` | `Resolver` seam · union · Workers AI · Claude · hybrid | ✅ |
| `internal/policy` | `EscalatePolicy` (default) · `PickWinnerPolicy` (incompatibility) | ✅ |
| `pkg/{models,gitx}` | shared types (incl. `models.Event` + `Emitter`) · git subprocess helpers | ✅ |
| `cmd/braid demo` · `internal/demo` | seeds a throwaway Go `/users` repo + 3 agents, integrates, prints verdict | ✅ |

**The two seams this plan builds on (already in the code, deliberately):**
1. `merge.Options.Emit func(models.Event)` + `models.Emitter{ Emit; Flush }` — the event stream seam.
   Today `cmd/braid demo` passes no `Emit`; nothing consumes events yet.
2. `ladder.Verifier interface { Verify(ctx, repoDir) (RungResult, error) }` — today `GoBuildVerifier`
   / `GoTestVerifier` shell out with `exec.CommandContext` **in the host process, in `repoDir`**
   (`internal/ladder/verify.go:runGo`, `GoTestVerifier.Verify`). This is the seam the sandbox slots
   behind. **Note:** `GoTestVerifier` parses the `go test -json` stream from *stdout* and detects
   "tests couldn't run" from a non-empty *stderr*, so the `Executor` must return stdout and stderr
   **separately** (not merged) or that rung's parse breaks.

**Already live — don't re-plan as new work:** `Integrate` runs the ladder via `Ladder.Run`
(`integrate.go:230,295,316`), which calls `StructuralGuards` **unconditionally**. So rung 1 **already**
blocks, in the live path, any test-file deletion or executable-test-count drop (per-file and total) —
the "delete the test to go green" shortcut is mechanically closed today for the Go test-count. What rung
1 is still missing (why `ARCHITECTURE_PRODUCTION.md` marks it 🟡) is in Phase 2a.

**What is missing** (from `ARCHITECTURE_PRODUCTION.md` §6 status matrix): everything 🟠 — the two-zone
sandbox, control-plane Worker (`/v1/runs`, authz, idempotency), Base-Coordinator DO, Queues+Workflow,
signed ledger, determinism/cache, and an operator console built against a real API. Rungs 0 and 5 too.

---

## 1. Constraints that reshape the product (read first)

- **The engine is a library, not a server — keep it that way.** `internal/merge.Integrate` stays a pure
  function over a `RepoDir`. Everything new (service, transport, sandbox, control plane) wraps it
  through the existing seams. Do **not** push HTTP/auth/queue concerns into `internal/merge`.
- **Two zones are a security invariant, not a deployment detail (C13, C15).** Untrusted code (agents,
  resolver output, the test suite) must run with **no credentials in the process** and default-deny
  egress; the Artifacts token lives only in a separate git-I/O path that never executes that code. The
  merged tree crosses the boundary as a **content-addressed bundle**, never as "the same working dir
  both zones can touch." This forbids the lazy design where the service clones, tests, and pushes in one
  process holding the token.
- **Tests are immutable merge inputs (C2).** `StructuralGuards` must reject any edit to test files or a
  drop in test count/coverage *before* execution. This is a gate, not a lint.
- **Determinism is a tested invariant, not an aspiration (C6).** Canonical content-hash order, temp-0 +
  pinned model digest, and a resolution cache keyed by `hash(base+ours+theirs+context+model+prompt-ver)`.
  CI merges identical inputs twice and asserts identical output hashes.
- **Go-first, pluggable toolchain (C22).** Verifiers are already an interface; a non-Go project plugs in
  its own build/test verifier later. Don't hard-code `go` beyond the Go verifiers themselves.
- **Every sub-chunk leaves `go test ./...` green; commit + push after each** (user preference — the
  contribution graph). `hackathon/` stays frozen and is never modified.
- **Console/operator UI is LAST**, built against the real `/v1/*` API — not first, and not the vanilla
  hackathon dashboard retrofitted. The 🟡 TS Orchestrator DO is a reference, not the foundation.

---

## 2. Target layout (new packages this plan adds)

```
cmd/braid/            ✅ demo  → + `serve` (run the engine as a service), `run` (one-shot against a real base)
internal/merge/       ✅ Integrate — unchanged spine
internal/ladder/      ✅ + verifiers call an Executor instead of exec'ing directly
internal/semantic/    ✅ unchanged; rung-0/5 contract work added later
internal/resolver/    ✅ + determinism: temp-0, pinned digest, resolution cache
internal/policy/      ✅ unchanged

internal/exec/        🟠 Executor seam: LocalExecutor (today's behavior) · SandboxExecutor (CF Sandbox/gVisor)
internal/run/         🟠 RunService: wraps Integrate, owns the Emitter, maps RunSpec→Options→Result
internal/event/       🟠 Emitter impls: HTTPEmitter (→ Orchestrator DO), + a seq-numbered buffer
internal/ledger/      🟠 signed, append-only SHA-256 chain record writer (R2 object-lock in prod)
internal/gitio/       🟠 git-I/O zone: scoped-token clone/push, parent-sha CAS; never runs user code
internal/determinism/ 🟠 canonical order, resolution cache, reproducibility harness

worker/               🟡 control plane: + /v1/runs lifecycle, authz, idempotency, Base-Coordinator DO, Queue consumer
dashboard/            🟡 operator console, rebuilt against /v1/* (LAST)
```

Nothing above moves `Integrate` out of `internal/merge`; it is always the thing being wrapped.

---

## 3. Phased roadmap — each phase has a hard exit gate

Themes track `PRODUCTION_PLAN.md` §7, but here as concrete Go deliverables with named packages. A phase
does not start until the previous gate is met and pushed.

### Phase 1 — Engine as a service + real test-execution sandbox ✅ DONE (1a–1d; gate met for dev backend)

*Rationale: the recommended resume step (status.md 2026-10-13). It turns the library into something a
control plane can call, and it makes "we ran your real tests" true in an isolated sandbox rather than on
the host. Nothing downstream can be trusted until untrusted code is actually contained.*

- **1a. `internal/exec` — the Executor seam. ✅ DONE (commit `75e8470`).** `Executor interface { Exec(ctx,
  Request) (Result, error) }` with `Request{ Dir, Argv, Env, EnvOverride, MergeOutput, EgressPolicy,
  Limits{CPU,Mem,Pids,Wall,Disk} }` and `Result{ Stdout, Stderr, Combined, ExitCode, OK }` (stdout/stderr
  split for the `go test -json` rung). `ladder.GoBuildVerifier`/`GoTestVerifier`/`runGo` call an injected
  `Executor`; nil defaults to `LocalExecutor` (today's exact behavior) so every caller is unchanged.
  Pure seam extraction — full suite + demo verdict unchanged.
- **1b. `SandboxExecutor`. ✅ DONE (commit `50910ea`).** Composes an inner `Executor` under a
  `SandboxBackend` with a credential-free environment: `Policy{EnvAllowlist, ExtraEnv, DenyEgress,
  Limits}` + `DefaultPolicy` (minimal non-secret env, `GOPROXY=off`, network denied, 5m wall);
  `SanitizeEnv` strips the host env to the allowlist (no token/API key reaches untrusted code) via
  `Request.EnvOverride`. Backends: **`SeatbeltBackend`** (macOS `sandbox-exec`, denies all network — the
  working dev isolation on this box, egress-deny proven against a live baseline) and `DirectBackend`
  (env-strip only, honest fallback). **Proven today:** credentials stripped + egress denied + the
  ladder's real build+test run GREEN inside the sandbox. **Still backend-provided (🟠):** filesystem
  confinement + CPU/mem/pids/disk caps need cgroups — they land with a Linux **container backend** (not
  gVisor on this macOS box) and the **CF Sandbox SDK** for prod, both slotting behind `SandboxBackend`
  with no rung change. Content-addressed tree-bundle input is still 🟠 (today the sandbox runs over `Dir`).
- **1c. `internal/run` — RunService. ✅ DONE (commit `08e98f5`).** `Service.Run(ctx, RunSpec) (RunResult,
  error)` builds `merge.Options` from a declarative `RunSpec`: resolver selection
  (union/auto/hybrid/workersai/claude), executor selection (local vs `SandboxExecutor` via the `Sandbox`
  flag), ladder wiring, and a best-effort `models.Emitter` (Flush at end). `cmd/braid serve` exposes it
  over HTTP (`POST /run`, `GET /healthz`; handler extracted as `serveMux` for tests) — the in-process
  transport the Worker calls later. `cmd/braid demo` reimplemented on top of `run.Service` (single code
  path) + a `--sandbox` flag; demo runs green both locally and sandboxed. Tests: run (local+sandbox
  end-to-end, validation, resolver select, emitter), serve (healthz/bad-body/422/full run over HTTP).
- **1d. `internal/event` — live event stream. ✅ DONE (commit `1b38a2c`).** `HTTPEmitter` (implements
  `models.Emitter`) best-effort POSTs `models.Event`s to the Orchestrator DO `/runs/:id/event`:
  seq-numbered (`models.Event.Seq`, for consumer gap-detection), ordered (single drain goroutine over a
  buffered queue), non-blocking (a full queue drops; the seq gap is the signal), with idempotent
  `Flush()`. Wired via the `Service.Emitter` seam: `RunSpec.DashboardURL` builds a per-run emitter
  (per-request streaming, correct under concurrent `serve`); `cmd/braid demo` passes `BRAID_WORKER_URL`
  through. Tests: event (order+seq, best-effort-on-unreachable, no-op, Flush) + a run-level stream to a
  stand-in DO. **Gap noted:** the Go engine emits `merge:start`/`verify`/… but not TS-style
  `run:init`/`run:done`; aligning the event vocabulary with the dashboard reducer is a Phase-5 task.

**Exit gate (Phase 1):** `braid serve` runs a full integration end-to-end with **build and tests
executed inside the SandboxExecutor**, streaming live events; `LocalExecutor` keeps `go run ./cmd/braid
demo` green and all `go test ./...` green; reproducibility of the *demo* verdict across sandbox vs local
is asserted. Red-team, split by what the dev backend can prove: **provable now (Seatbelt, ✅)** — no
token in the sandbox env, network egress denied (both tested in `internal/exec` + `internal/ladder`);
**provable on the container/CF backend (🟠)** — no host-filesystem access outside the job bundle and
enforced CPU/mem/pids/disk caps (Seatbelt leaves fs/resources open). The gate is **met for 1a–1d + the
env/egress red-team on the dev box**, and re-run in full when the container backend lands (its own
chunk, before Phase 3's prod claims).

### Phase 2 — Correctness core: rungs 0/1/5, determinism, tree merge 🟠

*Theme: the verification ladder is the heart; make it complete and reproducible.*

- **2a. Rung 1 hardening (`ladder.StructuralGuards`, 🟡→✅) — only the gap, the core is already live.**
  Test-file deletion and executable-test-count regression are **already enforced** by `Ladder.Run` (see
  §0). What remains: (i) **confine resolver edits to the conflict region** (reject a resolution that
  touches anything outside the hunk it was given); (ii) **AST bounds on LLM output** (resolver output
  must parse and stay within declared bounds — a C14 defense); (iii) **real coverage** non-regression,
  not just static test-count — the static `CountTestFuncs` can't see `t.Skip()`-based weakening (a known
  gap the code comments call out); close it with Keploy coverage deltas.
- **2b. Rung 0 — contract-first (`internal/semantic` or new `internal/contract`).** Each subtask carries
  a machine-readable acceptance spec + tests, frozen and content-hashed before code. The feature's test
  exists before its code.
- **2c. Rung 5 — metamorphic contract preservation.** Golden snapshots of each agent's observable
  behavior on its own fork; assert the merged system still satisfies *every* agent's contract ("both
  features survived").
- **2d. Determinism (`internal/determinism` + `internal/resolver`).** Canonical content-hash agent
  order; temp-0 + pinned model digest in the LLM resolvers; resolution cache keyed by
  `hash(base+ours+theirs+context+model-digest+prompt-ver)`.
- **2e. Parallel tree merge.** Fold N agents as a pairwise tree of depth O(log N), each node an
  independent sandboxed job; full suite always at the root.

**Exit gate (Phase 2):** a multi-agent conflict corpus (including a *semantic, no-textual-conflict* case)
merges correctly; a resolution that edits outside its conflict region or emits non-parsing output is
rejected by a test (the test-count/delete-test block is already live from Phase 0 — this gate adds the
region-confinement + AST-bounds cases); **reproducibility CI merges identical inputs twice across N
agents and asserts identical output hashes.**

### Phase 3 — Control plane + git-I/O zone (the trusted boundary) 🟠

*Theme: a real API in front of the engine, and the only place the Artifacts token lives.*

- **3a. `internal/gitio` — the trusted zone.** Holds a short-lived, single-operation-scoped Artifacts
  token; clones the base and pushes the merged bundle; **never executes untrusted code.** The merged
  tree enters as a content-addressed bundle from the compute zone.
- **3b. `worker/` control plane (🟡→✅).** `POST /v1/runs` (fork N, enqueue), `GET /v1/runs/:id`,
  `POST /v1/runs/:id/{cancel,approve}`; authN/Z + RBAC + capability tokens; **idempotency key** so a
  retry can't double-run or double-charge; per-run HMAC on `/runs/:id/event`. JIT-minted scoped tokens
  with auto-refresh (C15).
- **3c. Queues + durable Workflow.** `POST /v1/runs` enqueues; a Workflow consumer drives
  `RunService.Run`; self-push event filter + dedup so the system can't trigger itself (C9).

**Exit gate (Phase 3):** an external client runs the whole pipeline through `POST /v1/runs` and gets a
green verdict + merged SHA on a **real Artifacts base**; a red-team pass proves injection in agent code
cannot exfiltrate a token, reach the network, or mutate tests, and that there is **zero unauthenticated
surface**.

### Phase 4 — Consistency & provenance 🟠

- **4a. Base-Coordinator DO.** One lease per base; **compare-and-swap on the parent commit SHA** so two
  runs can't clobber each other (C8). `internal/gitio` push goes through it.
- **4b. Signed ledger (`internal/ledger`).** External, append-only, **SHA-256 chain**, object-locked R2;
  every step writes a signed record (input hashes, resolution, model digest, test results); merges are
  signed (C17/C18). The in-repo `.braid/trail.json` stays as the human-readable view.
- **4c. Orchestrator DO (🟡→✅).** Seq-numbered events, snapshots, blobs→R2; rebuildable; git stays the
  single source of truth with the DO as an outbox (C10/C11).

**Exit gate (Phase 4):** a concurrency fuzz shows **no non-fast-forward data loss, no self-trigger loop,
no duplicate runs**; the ledger is demonstrably tamper-evident (a mutated record is detected).

### Phase 5 — Operator console + resilience + cost 🟠 (console LAST)

- **5a. Operator console (`dashboard/`, rebuilt).** Run list, live WebSocket view, escalation review,
  audit lineage — built against `/v1/*` and the ledger, not the hackathon dashboard retrofitted.
- **5b. Resilience.** DR mirror of the Artifacts base, compaction, auto-rollback on post-merge
  correctness regression, trust-tier policy.
- **5c. Economics.** Per-run metering + budgets; publish **$/run** and gate on it being positive.

**Exit gate (Phase 5):** an operator drives a run start→approve-escalation→audit entirely from the
console; a restore drill proves RPO/RTO; auto-rollback is demonstrated; **$/run is published and
positive.**

### Phase 6 — Scale hardening 🟠

Sandbox pool autoscale + back-pressure + per-tenant fairness; load test.

**Exit gate (Phase 6):** sustained target throughput at bounded p99 with no noisy-neighbor starvation.

---

## 4. Sequencing of the next working session (Phase 2, small green chunks)

*Phase 1 is DONE (1a–1d; see §3). Resume at Phase 2 — rung-1 hardening first, as it builds directly on
the already-live `StructuralGuards`.*

1. **2a Rung-1 hardening** (`internal/ladder`): (i) confine resolver edits to the conflict region — add a
   guard that rejects a resolution touching anything outside the hunk it was handed, with a test that a
   stray out-of-hunk edit is rejected; (ii) AST bounds on LLM/resolver output — output must parse (`go/
   parser`) and stay within declared bounds (C14), with a non-parsing-output-rejected test. (The
   delete-test / test-count-regression block is ALREADY live via `Ladder.Run`→`StructuralGuards` — do not
   rebuild it.) Commit + push.
2. **2d Determinism** (`internal/determinism` + `internal/resolver`): canonical content-hash agent order;
   temp-0 + pinned model digest in the LLM resolvers; resolution cache keyed by
   `hash(base+ours+theirs+context+model-digest+prompt-ver)`. Add a reproducibility test (identical inputs
   → identical output hash, run twice). Commit + push.
3. **2e Parallel tree merge**: fold N agents as a pairwise tree of depth O(log N), each node an independent
   sandboxed job; full suite always at the root. Commit + push.
4. **2b/2c Rungs 0 & 5** (ship minimal first — §5 q4): rung 0 = acceptance spec frozen + content-hashed
   before code; rung 5 = metamorphic golden snapshots asserting every agent's contract survives the merge.
   Commit + push.
5. Meet the Phase 2 exit gate (conflict corpus merges correctly incl. a semantic no-textual-conflict case;
   region-confinement + AST-bounds rejection tests; reproducibility CI asserts identical output hashes
   twice across N agents). Update `status.md`.
   - *Optional, can slot before/within Phase 2:* the **Linux container / CF Sandbox backend** behind
     `exec.SandboxBackend` (cgroups → fs-confinement + CPU/mem/pids/disk caps + content-addressed bundle
     input) — closes the 🟠 half of the Phase 1 gate before Phase 3's prod claims. Needs a Linux host
     (this box is darwin arm64, Docker daemon down), so it may wait for the deploy target.

---

## 5. Open questions to resolve before/within each phase

1. **Sandbox substrate for the gate.** CF Sandbox SDK is the prod target, but is a local gVisor/container
   `LocalSandboxExecutor` acceptable to *prove* Phase 1's gate without cloud? (Plan assumes: yes — dev
   impl for the gate, CF Sandbox for prod, both behind the `Executor` seam.)
2. **Bundle format across zones.** Git bundle vs packfile vs tar-of-tree for the content-addressed
   hand-off between compute and git-I/O zones. (Plan assumes: git bundle — reuses `gitx`.)
3. **LLM determinism.** Temp-0 + pinned digest is necessary but not sufficient (provider nondeterminism).
   Is the resolution cache the real reproducibility guarantee, with temp-0 best-effort? (Plan assumes: yes.)
4. **Rung 0/5 scope.** Full contract-first authoring is large. Ship a minimal "acceptance spec is frozen
   + content-hashed before code" first, metamorphic snapshots after? (Plan assumes: yes, split.)
5. **Where RunService lives at runtime.** `cmd/braid serve` as a standalone binary the Workflow calls, vs
   compiling the engine into the sandbox image the Workflow spawns. (Decide at Phase 3.)

---

## 6. Invariants to preserve through the whole productionization

- `internal/merge.Integrate` stays a pure library function over a `RepoDir`; servers/transports wrap it.
- Every sub-chunk leaves `go test ./...` green; commit + push after each (contribution graph).
- `hackathon/` is frozen and runnable; never modified.
- Tests are immutable merge inputs; verdicts stay structured (`green/red/inconclusive/incompatible`).
- No credential ever enters the zone that runs untrusted code; the token lives only in `internal/gitio`.
- Determinism is a tested invariant (identical inputs → identical output hash), not a hope.
- Composition over selection: the default incompatibility policy drops nothing and flags a human.

---

*Grounded in the live Go module (`github.com/KC1706/braid`), `ARCHITECTURE_PRODUCTION.md`, and
`PRODUCTION_PLAN.md` §4–7. Phase 1 is DONE; **Phase 2 (correctness core) is the resume point** — start at
2a rung-1 hardening (status.md 2026-10-13).*

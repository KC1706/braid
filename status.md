# Status — Braid (Cloudflare Git-for-Agents Hackathon)

> Cross-session memory. Update this at the end of every work session: what changed, what's next,
> what's blocked. Newest entry on top. See `plan.md` for the full plan.

**Deadline:** 2026-10-14 · **Today:** 2026-10-12 · **Phase:** PIPELINE + LIVE DASHBOARD DEPLOYED; PRODUCTION PLAN WRITTEN

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

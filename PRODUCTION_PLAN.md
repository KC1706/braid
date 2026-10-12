# Braid — Production-Grade Plan

> Finalised engineering plan to take Braid from a proven hackathon demo to a production-grade,
> horizontally-scalable, trust-worthy multi-agent merge platform on Cloudflare Artifacts.
>
> Audience: engineering + infra. Status: design (no code committed from this plan yet).
> Last updated: 2026-10-12. Companion docs: `plan.md` (original), `status.md` (session handoff).

---

## 0. Executive summary

Braid is the **coordination + review + test-verified merge layer** on top of Cloudflare Artifacts:
a swarm of agents produce divergent branches; Braid composes them into one result and proves it with
real verification, rather than making a human pick a winner.

The hackathon build proves the thesis end-to-end on real Artifacts (fork-per-agent → merge → re-test →
push back, with a live dashboard). This document is the honest gap analysis between that demo and a
production system, and the architecture that closes it.

**Two foundational decisions drive everything else:**

1. **The customer is the machine.** API-first for agents/orchestrators. Humans are *operators,
   auditors, and approvers of escalations* — never on the per-merge critical path. This right-sizes
   the frontend to an ops/audit/policy/escalation console (not a GitHub clone) and resolves the
   autonomy-vs-review tension.
2. **"Green" is a multi-layer verdict, and test files are immutable merge inputs.** A single exit
   code is never the gate. Verification is a ladder (§4). The merge engine is structurally forbidden
   from editing tests or reducing coverage.

**The core insight:** the storage layer is *not* the risk — Cloudflare solved that. Braid's real
product is a **correctness, determinism, and trust engine**. That is where the engineering goes.

---

## 1. What Cloudflare Artifacts actually is

Beyond the "Git for agents" framing, Artifacts is:

> A versioned filesystem that speaks the Git wire protocol, where **every repository is its own
> globally-distributed Durable Object**, created and controlled programmatically.

Architecture (from Cloudflare's engineering blog):

| Layer | Role |
|---|---|
| **Worker (front door)** | Auth/authz, metrics, routes each request to the repo's Durable Object |
| **Durable Object (one per repo)** | The repo *is* a DO. "Millions of isolated instances, one per repository." Refs/HEAD are strongly consistent here |
| **SQLite inside the DO** | Git objects stored via the sync KV API; objects chunked because DO rows cap at **2 MB** |
| **Zig→WASM git engine** | Hand-written Git protocol engine (~100 KB WASM): SHA-1, zlib, delta encoding, pack parsing, smart HTTP v1+v2, shallow/blobless clone, have/want negotiation |
| **R2** | Snapshots / pack bundles |
| **KV** | Auth-token tracking |

Facts that shape the design:
- **~128 MB DO memory** → everything streams (push and fetch return `ReadableStream`); you cannot load
  a repo into memory.
- **Deltas are persisted, not recomputed** server-side.
- **Programmable primitives, not a product:** `create`, `fork(name,{readOnly})`, `get`, `import`,
  repo-scoped token minting, `readFile`, `log`, `info`, plus a REST API.
- **Events:** repo created/imported/forked/deleted/**pushed**/cloned/fetched → Worker or **Queue**,
  with per-subscription retry + dead-letter queues. Canonical pattern:
  `cf.artifacts.repo.pushed → Queue → review Workflow`.
- **Workers Builds:** push to production branch → deploy; other branches → preview URLs.
- **Jurisdiction:** US/EU pinned per namespace.
- **Limits / pricing:** **1 GB per repo**; 10k ops + 1 GB free/month, then **$0.15/1k ops** and
  **$0.50/GB-month**. Open beta, Workers Paid only; billing starts 2026-10-15.

**What it deliberately is NOT:** no PRs, code review, merge, diff UI, issues, orgs/teams, or access
model beyond tokens. Cloudflare explicitly does *not* want "GitHub as it exists today with agents
bolted on" — they ship storage primitives and leave coordination/review/merge (Braid's layer) to builders.

---

## 2. How Artifacts differs from GitHub

The difference is architectural, and it is the whole reason the scaling question is interesting.

| Dimension | **GitHub** | **Cloudflare Artifacts** |
|---|---|---|
| Unit of scale | Repo = **3 replicas on 3 file servers** (Spokes/DGit); write commits when ≥2 confirm | Repo = **1 Durable Object**, globally addressable, auto-placed |
| Scaling model | Grow a server fleet; balance repos across hosts | **Millions of independent DOs**; no fleet to balance |
| Provisioning | Heavyweight, rate-limited repo creation | **Programmatic, cheap, per task/agent/session/user** |
| Compute location | CI runs elsewhere, pulls repo over the network | Compute (Workers/Sandboxes) runs **adjacent to** the repo DO |
| Collaboration | PRs, review, merge, issues, orgs built in | **None — you build it** |
| Consistency | App-level replication; reads from nearest replica | DO = **strong single-writer** per repo for refs/HEAD |
| Pricing | Per-seat (humans) | Per-operation + per-GB (machines) |
| Egress | — | **Zero egress** |

**One sentence:** GitHub scales a *fleet of servers* hosting repos for *humans* collaborating through a
product; Artifacts makes *each repo an independent serverless object* so you can mint *millions cheaply
for machines*, and leaves you to build the collaboration product.

**Important caveat (strategic):** "scale to GitHub's user numbers" conflates **repo-hosting scale**
(which Artifacts gives you) with **GitHub's actual moat** — the human network, identity, discovery, and
social graph. Artifacts scales repos-for-machines; it does nothing for the network-of-humans. Decision 1
(§0) accepts this: the customer is the machine.

---

## 3. Scaling analysis — can it reach GitHub scale?

**Storage/provisioning: yes, arguably better than GitHub's model.** DO-per-repo is purpose-built for
millions of repos with automatic placement and no fleet to shard.

**Three hard platform limits to design around:**
1. **1 GB/repo** → many small, short-lived repos (repo-per-task), never giant monorepos.
2. **~128 MB DO memory + 2 MB rows + single-writer per repo** → never let N agents write one repo;
   fan out across forks (one DO each) and fan in through a coordinator. (This is exactly Braid's grain.)
3. **Per-operation billing + beta API churn** → cost-aware batching/caching are first-class; isolate the
   Artifacts API behind a thin adapter.

**Where the real bottleneck is:** not storage — the **fan-in** (merge + re-run real tests). GitHub scale
means millions of merge jobs/day, each cloning + merging + testing. That is a **job-queue + sandbox-pool
scaling problem**, and it is where ~90% of production engineering effort goes. Braid is GitHub-scale only
if the integration engine is a horizontally-scalable, idempotent, back-pressured, deterministic job system.

---

## 4. Critical flaws in the naive design (and the gate each fix must pass)

The demo passes a design review and then fails in production for these reasons. Each is addressed in §5.

### Correctness
- **C1 — The clean-merge path is the dangerous one.** Semantic conflicts with *no textual overlap*
  (A renames a symbol, B calls it elsewhere) auto-merge "clean" and break silently. The engine only
  resolves *textual* conflicts and **aborts** on a clean-merge test failure. The common real failure is
  unhandled; the rare synthetic one is lovingly handled.
- **C2 — The cheapest way to green is to delete the test.** Exit-code gate + an LLM told "make tests
  pass" → it will discover that removing assertions / `.skip` / deleting the file is the shortest path.
  Nothing detects test-count or coverage regression.
- **C3 — "Green" ≠ correct.** Baseline tests prove "didn't break what existed," not "the new features
  are right." Who writes tests for agent-added features? Composed code can pass by accident and still be
  semantically wrong (ordering bugs, conflicting contracts).
- **C4 — Union is not a law.** Real tasks produce mutually exclusive outputs; forcing composition is
  sometimes nonsense even when green. Need an incompatibility / pick-a-winner path.

### Determinism & scale of the fan-in
- **C5 — Serial O(N) full-suite folds.** N agents = N+ full test runs, sequential → latency wall, not a
  throughput story.
- **C6 — Order-dependent + non-reproducible.** Fold order changes the result and even the verdict;
  LLM nondeterminism (temperature, silent model upgrades) means identical inputs diverge. The
  idempotency key `(base, fork shas, resolver version)` is a fiction.
- **C7 — Fragile conflict splicer.** Hand-rolled diff3 parser + positional hunk mapping corrupts on
  CRLF/BOM/binary, marker-like content, hunk-count changes, and coupled hunks resolved independently.

### Concurrency & consistency
- **C8 — Base/main write race.** Two runs pushing to one base → non-fast-forward reject after paying for
  the full merge. No lock.
- **C9 — Self-triggering event loop.** The integrator's push to base emits `pushed` → triggers another
  integration → infinite, per-op-billed loop. At-least-once delivery also duplicates runs.
- **C10 — Orchestrator DO: quadratic writes + silent corruption.** `put(wholeState)` per event is
  O(events × state_size) and marches toward 128 MB; fire-and-forget, order-assuming `reduce()` corrupts
  silently on dropped/reordered events.
- **C11 — Two sources of truth.** Git state vs metadata store diverge on partial failure.

### Security
- **C12 — Open control plane.** Endpoints appear unauthenticated → anyone can create repos, mint write
  tokens, run Workers AI, and inject fabricated run events. Open relay + cost-DoS + state spoofing.
- **C13 — Untrusted code + a write token in the room.** Agent code + `npm install` postinstall + test
  code = RCE by design, with an Artifacts write token present to exfiltrate.
- **C14 — Prompt-injection → RCE chain.** Agent file content/comments/intent flow into the resolver
  prompt; its output is spliced into code and executed by the test sandbox.
- **C15 — Token lifetime vs runtime; blast radius.** 3600 s TTL can expire mid-run; the integrator holds
  *every* fork token + base token at once → one compromise = write to everything.
- **C16 — Supply chain vs 1 GB.** Can't commit node_modules → cold, networked, flaky `npm install` per
  run = latency + a fresh supply-chain exposure each time.

### Trust, reliability, economics, observability
- **C17 — In-band, forgeable provenance.** `trail.json` is committed into a writable repo; authorship is
  arbitrary `git user.name`. Not tamper-evident; not real provenance.
- **C18 — SHA-1 trust anchor.** Collision-weak hash underpinning a provenance/security substrate for
  adversarial agents.
- **C19 — Blind on correctness in prod.** All metrics measure the gate, not reality; "green rate" gets
  gamed by C2 and reveals nothing about true correctness.
- **C20 — No DR on a beta primitive.** Source of truth lives entirely in a beta with unstated durability
  and a 1 GB cap. No backup/rollback story.
- **C21 — No unit-cost model.** Per-op billing + sandbox compute + LLM tokens, at millions of runs/day,
  with no $/run analysis. Flaky tests + retries amplify cost.
- **C22 — Single-language assumption.** Everything is JS/vitest; production is polyglot. The verifier
  must be a pluggable per-repo toolchain.

---

## 5. Production architecture (how each flaw is resolved)

### 5.1 The verification ladder — the heart (fixes C1, C2, C3, C4, C22)

Replace "run suite, check exit code" with a deterministic, cheapest-first ladder. Tests are
necessary-not-sufficient, and **immutable during merge**.

- **Rung 0 — Contract-first decomposition.** Each subtask carries a machine-readable acceptance spec +
  tests authored by a dedicated test-author agent and **frozen (content-hashed, signed) before coding**.
  Tests are first-class artifacts. (Fixes C3: the feature's test exists before its code.)
- **Rung 1 — Structural guards (deterministic, pre-execution).** Resolution diff confined to the
  conflict region; **reject any change to test files or any test-count/coverage regression** (fixes C2);
  AST bounds on LLM output — no new top-level imports/`require`/shell-outs unless allowlisted (first line
  against C14).
- **Rung 2 — Build + typecheck + lint.** Cheap; catches most semantic breakage without running anything.
- **Rung 3 — Semantic-conflict detection (fixes C1).** Symbol/dependency graph of base vs each fork; if
  A changes/renames/removes a symbol B references — even with no textual conflict — force that pair into
  the resolution path instead of trusting the clean auto-merge.
- **Rung 4 — Test execution, deterministically selected.** Impacted tests at internal nodes; **full
  suite always at the root** so a selection miss can't yield a false final green. Flakes quarantined,
  bounded-retried, and treated as *inconclusive* (never green/red).
- **Rung 5 — Contract/metamorphic preservation.** Golden snapshots of each agent's observable behavior
  on its own fork; assert the merged system still satisfies *each* agent's contract. This proves "both
  features survived," not merely "old tests pass."
- **Incompatibility path (fixes C4).** On provably mutually-exclusive contracts or persistent red after
  bounded retries, emit an `incompatible` verdict, apply policy (priority / arbiter agent / human
  escalation), and record the discarded candidate. Union is one strategy, not the law.
- **Pluggable toolchain (fixes C22).** Per-repo detector selects build/test runner; the ladder is
  language-agnostic, runners are adapters.

### 5.2 Deterministic, parallel fan-in (fixes C5, C6, C7)

- **Canonical ordering** by content hash (never arrival/fetch order).
- **Parallel tree merge** (pairwise reduction, O(log N) depth); each node an independent, sandboxed,
  cacheable job.
- **Reproducible resolution:** LLM at **temperature 0**, **pinned model digest** (no floating aliases),
  fixed prompt version. Resolution cache keyed by
  `hash(base + ours + theirs + surrounding-context + model-digest + prompt-version)`, replayed only into
  byte-identical context. Now the idempotency key is a true function of inputs (fixes C6).
- **Determinism is a tested invariant:** CI runs representative merges twice and asserts identical output
  hashes.
- **Robust conflict handling (fixes C7):** structural guards (§5.1 Rung 1) + fuzzed parser against
  real-world files (CRLF/BOM/binary/marker-like content/coupled hunks); coupled hunks resolved with
  cross-hunk context, not independently.

### 5.3 Security: two-zone execution + authenticated control plane + hostile supply chain

- **Two-zone sandbox (fixes C13, C14, C15):**
  - *Untrusted compute zone* — runs agent code + resolver output + tests. **No credentials,
    default-deny egress** (allowlisted registry mirror only), hard CPU/mem/pids/disk/wall-clock caps,
    microVM/gVisor-class isolation, ephemeral per job. Injected code fails the gate in a box with nothing
    to steal and nowhere to call.
  - *Git-I/O zone* — holds a short-lived, **single-operation-scoped** token; clone/push only; no
    untrusted code. The merged tree moves between zones as a content-addressed bundle; tokens never enter
    the compute zone.
- **Control-plane authn/z (fixes C12):** OIDC / per-org API keys + org→project→run RBAC + per-tenant
  quotas + idempotency keys + WAF/rate-limit. Run endpoints use capability-scoped run tokens; event
  ingestion requires a per-run HMAC; `/state` and `/ws` require a read capability.
- **Hostile `npm install` (fixes C16):** private registry proxy + allowlist, lockfile-pinned,
  **`--ignore-scripts` by default**, content-addressed dependency cache keyed by lockfile hash →
  warm, offline, deterministic installs.
- **Tokens (fixes C15):** JIT-minted, narrowly scoped per operation, auto-refreshed for long runs,
  never aggregated into one process.

### 5.4 Provenance & trust (fixes C17, C18)

- **Externalize provenance** into a signed, append-only ledger (object-locked R2 + hash chain, SHA-256).
  Each step writes a signed record: input content-hashes, strategy, pinned model digest, test-result
  hashes, sandbox attestation. In-repo `trail.json` remains only a clearly-labeled, non-authoritative
  projection.
- **Platform signs the merge** with its own key; authorship attributed to the integrator + a ledger
  reference, not forgeable `git user.name`; agent contributions referenced by content hash.
- **SHA-1 never the trust anchor (fixes C18):** ledger uses independent SHA-256 of blobs/trees; fetched
  objects cross-checked to detect collision attempts.

### 5.5 Consistency & concurrency (fixes C8, C9, C10, C11)

- **Base-Coordinator DO (fixes C8):** one integration lease per base repo; optimistic concurrency via
  expected-parent-sha compare-and-swap; on reject → rebase-and-reverify, never blind retry.
- **Event-loop break + dedup (fixes C9):** platform pushes carry bot identity + internal ref namespace
  (`refs/braid/*`) + commit trailer; the Queue consumer filters self-authored writes and reacts only to
  an explicit allowlist; consumer idempotent keyed by `(repo, commit-sha)`.
- **Orchestrator DO rebuild (fixes C10):** producer-assigned monotonic sequence numbers; gap-tolerant,
  reorder-by-seq `reduce()` with replay-on-gap; **event-log + periodic snapshot** (not
  whole-state-per-event); large blobs in R2, DO holds references; acked/retried events, not
  fire-and-forget.
- **Single source of truth (fixes C11):** git is authoritative; metadata is a derived projection updated
  via an outbox/reconciliation pattern so partial failures self-heal.

### 5.6 Platform limits & DR (fixes C20, and the 1 GB/GC problem)

- **Topology:** many small, short-lived repos (repo-per-task); durable state lives in the ledger + R2,
  not in long-lived giant repos.
- **Compaction:** monitor per-repo object growth as a first-class metric; near threshold, **compact by
  re-import** (import HEAD into a fresh repo, archive full history to R2).
- **DR (fixes C20):** on every successful merge, mirror base + merged result to external durable storage
  (object-locked R2 and/or a second git remote). RPO = last successful merge; RTO documented via
  re-import. The beta is never the only copy.

### 5.7 Correctness observability, rollback, trust tiers (fixes C19)

- **Proxy the unmeasurable:** (a) gate-strength metrics — test-count/coverage deltas per merge, any drop
  alerts (also enforces C2); (b) **staging ref + extended checks** (property/fuzz/contract) before
  promotion to main; (c) **post-merge runtime watch** with auto-rollback on contract-violation/error-rate
  spikes attributable to a merge; (d) **golden audits** — human-reviewed random sample to estimate true
  correctness and calibrate trust tiers.
- **Rollback:** every main push is signed, reversible, with a known parent → bad merge auto-reverts to
  last-good and quarantines the run.
- **Trust-tier policy engine:** classify each run by risk (repo criticality, diff blast radius, test
  coverage of the change, incompatibility detected) → auto-merge low-risk/high-confidence; human
  escalation only for incompatibilities, coverage drops, or low-trust repos.

### 5.8 Economics (fixes C21)

Meter ops + sandbox-seconds + LLM tokens per run; enforce per-run and per-tenant **hard budgets**
(fail fast, never silent overspend). Deterministic caches (resolution, dependency, test-selection) and
blobless clones attack the dominant cost lines. Measure the union-resolver hit rate on *real* conflicts
and drop it if low. **$/run published before GA; no GA without positive unit economics.**

### 5.9 Frontend (operator/auditor console, per Decision 1)

Next.js (React) on Cloudflare Pages, TypeScript, Tailwind + component lib. Surfaces: run list/dashboard;
live run view (productionized swim lanes + conflict panel + provenance trail, DO→WebSocket with
reconnect/replay); repo/fork explorer with diffs; **escalation merge-review** (per-hunk provenance:
which agent, strategy, attempts, verdict; approve/override — for escalations only); provenance/audit
lineage graph; org/project/RBAC/quota/usage. Auth via Cloudflare Access / Auth.js; Artifacts tokens
never reach the browser.

---

## 6. Target architecture (diagram)

```
API-first clients (agents/orchestrators)        Human console (ops/audit/escalation only)
            | authenticated (OIDC/API key, RBAC, quotas)       | read-cap / approve-cap
            v                                                   v
+------------------------- Control-plane Worker -------------------------------+
| authN/Z · idempotency · policy/trust-tier engine · cost governor             |
+------+----------------+---------------------------+----------------+---------+
       | events          | serialize base writes     | enqueue        | ledger
       v                 v                            v                v
  Artifacts        Base-Coordinator DO          Cloudflare Queues   Signed append-only
  (repo/fork,      (lease + CAS on               (self-push          ledger (R2 object-lock
   DO per repo)     expected-parent-sha)          filtered, dedup)    + hash chain, SHA-256)
       |                                             |
       |                                             v
       |                           +-------- Integration Workflow (durable) --------+
       |                           | canonical order -> parallel TREE merge          |
       |                           | per node: verification ladder (rungs 0-5)       |
       |                           +------+-----------------------------+------------+
       |                     git-I/O zone (token, no code)        compute zone (no creds,
       |   mirror on success ->      |  clone/push bundle           egress-deny, capped,
       v   external R2 / 2nd remote  |                              ephemeral; code+tests)
  Orchestrator DO per run  <-- acked, seq-numbered events; event-log + snapshots; blobs in R2
```

---

## 7. Phased roadmap — each phase has a hard exit gate

| Phase | Theme | Exit gate (no advance until met) |
|---|---|---|
| **0. Ship the demo** | Hackathon submission | Video + submit (current). No arch change |
| **1. Correctness core** | Verification ladder (rungs 0–3), test-immutability + coverage non-regression, semantic-conflict detection, incompatibility path | Real multi-agent conflict corpus (incl. semantic/no-textual) merges correctly; "delete-the-test" attack mechanically blocked |
| **2. Determinism & parallelism** | Canonical order, tree merge, temp-0 + pinned model, resolution cache, fuzzed splicer | Reproducibility CI: identical inputs → identical output hash, twice, across N agents |
| **3. Security** | Two-zone exec, control-plane authn/z, hostile npm, JIT scoped tokens | Red-team: injection cannot exfil a token, reach network, or mutate tests; zero unauthenticated surface |
| **4. Consistency & provenance** | Base-Coordinator CAS, event-loop filter + dedup, Orchestrator rebuild, signed external ledger + signed merges | Concurrency fuzz: no non-fast-forward data loss, no self-trigger loop, no duplicate runs; ledger tamper-evident |
| **5. Resilience & cost** | DR mirroring, compaction, correctness observability, rollback, trust-tier policy, unit-cost model | Restore drill proves RPO/RTO; auto-rollback demonstrated; **$/run published and positive** |
| **6. Scale hardening** | Sandbox pool autoscale + back-pressure + per-tenant fairness, load test | Sustained target throughput, bounded p99, no noisy-neighbor starvation |

---

## 8. Critique → fix traceability

| # | Critique | Resolved in |
|---|---|---|
| C1 | Clean-merge semantic breakage; abort-on-clean-fail | §5.1 Rung 3 + incompatibility path |
| C2 | Deleting tests is the cheapest green | §0 Decision 2 + §5.1 Rung 1 |
| C3 | "Green" ≠ correct; untested features | §5.1 Rung 0 + Rung 5 |
| C4 | Union is not a law | §5.1 incompatibility path |
| C5 | Serial O(N) full-suite folds | §5.2 parallel tree merge |
| C6 | Order-dependent / non-reproducible | §5.2 canonical order + temp-0 + pinned model |
| C7 | Fragile conflict splicer | §5.1 Rung 1 + §5.2 fuzzed/cross-hunk |
| C8 | Base/main write race | §5.5 Base-Coordinator CAS |
| C9 | Self-triggering event loop + dupes | §5.5 event filter + idempotent consumer |
| C10 | Orchestrator DO quadratic writes + corruption | §5.5 seq events + event-log/snapshot + R2 blobs |
| C11 | Two sources of truth | §5.5 git authoritative + outbox |
| C12 | Open control plane | §5.3 authn/z + capability tokens + HMAC |
| C13 | Untrusted code + write token present | §5.3 two-zone exec |
| C14 | Prompt-injection → RCE | §5.1 Rung 1 AST bounds + §5.3 no-cred/egress zone |
| C15 | Token TTL vs runtime; blast radius | §5.3 JIT scoped tokens, auto-refresh, git-I/O zone |
| C16 | Supply chain vs 1 GB | §5.3 hostile-npm cache |
| C17 | In-band forgeable provenance | §5.4 external signed ledger + signed merges |
| C18 | SHA-1 trust anchor | §5.4 independent SHA-256 + collision check |
| C19 | Blind on correctness in prod | §5.7 gate-strength, staging ref, post-merge watch, audits |
| C20 | No DR on a beta primitive | §5.6 external mirror + documented RPO/RTO |
| C21 | No unit-cost model | §5.8 metering + budgets + $/run gate |
| C22 | Single-language assumption | §5.1 pluggable toolchain |

---

## 9. Open questions / risks to resolve before committing

1. **Artifacts beta churn + unstated durability** — isolate behind an adapter; mirror externally; do not
   treat as the only copy.
2. **LFS / large binaries** — support unconfirmed; validate against target repo shapes.
3. **Union-resolver real-world hit rate** — measure before relying on it; may be near-zero on true
   conflicts.
4. **Test-author agent quality** — Rung 0 hinges on it; needs its own evaluation harness.
5. **Sandbox pool economics** — the dominant cost/latency line; model early (§5.8).
6. **Semantic-conflict graph across languages** — the per-language analysis depth for Rung 3 is
   non-trivial; stage by language.
7. **Human escalation throughput** — trust-tier thresholds must keep humans off the critical path while
   catching true incompatibilities; calibrate with golden audits.

---

## 10. Sources

- Cloudflare — [Artifacts: versioned storage that speaks Git](https://blog.cloudflare.com/artifacts-git-for-agents-beta/)
- Cloudflare — [We want you to build the next Git platform on Cloudflare](https://blog.cloudflare.com/next-git-platform-on-cloudflare/)
- Cloudflare — [Artifacts product page](https://www.cloudflare.com/products/artifacts/)
- Cloudflare — [Artifacts now in beta (changelog)](https://developers.cloudflare.com/changelog/post/2026-04-16-artifacts-now-in-beta/)
- DEV — [What Building a Git Platform for AI Agents Reveals About Event-Driven Plumbing](https://dev.to/mech_app_ai/cloudflare-artifacts-what-building-a-git-platform-for-ai-agents-reveals-about-event-driven-pm7)
- DEV — [A Repo per Agent: What Cloudflare Artifacts Changes About Git](https://dev.to/devopsdaily/a-repo-per-agent-what-cloudflare-artifacts-changes-about-git-3m5b)
- GitHub Engineering — [Introducing DGit (Spokes / 3-replica model)](https://github.blog/engineering/architecture-optimization/introducing-dgit/)

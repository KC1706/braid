# Braid — Cloudflare "Git for Agents" Hackathon Plan

> **One-liner:** Many AI agents work on **one** codebase concurrently, each in its own
> cheap Artifacts fork; a hybrid merge engine **composes** their complementary work into a
> single verified codebase — reconciling true conflicts with an LLM and proving every change
> green by running real tests. Not a tournament. A merge.

- **Competition:** Cloudflare — build the next Git platform for the agentic era, on Workers + Artifacts.
- **Deadline:** **2026-10-14** (plan written 2026-10-10).
- **Deliverables:** 5–10 min demo video · source under MIT/Apache/BSD · run instructions.
- **Prize:** Top 3 → flown to SF for Cloudflare Connect; 1st → $25,000 Cloudflare credits + VIP dinner.
- **Chosen direction (locked):** Concept A (swarm + AI merge) · Claude via AI Gateway · tight vertical slice.
- **Working name:** **Braid** (alts: Confluence, Loom, Concord).

---

## 0. Why the obvious entry loses

Most teams will build **best-of-N / a tournament**: spawn N agents, run them, keep the single
best, discard the rest. That wastes N−1 agents and never actually solves *concurrent changes to
one repo* — the literal thing Cloudflare asked for. Repeated sampling is high-variance (SWE-Search),
which is *why* people sample many, but Cloudflare's framing is **many agents on one codebase**, not
a contest.

**Our contrarian thesis = composition, not selection.** N agents do complementary pieces of one
task; we **combine** their work and auto-reconcile the collisions. "They built a tournament, we
built a merge" is the demo's winning line.

---

## 1. Cloudflare's 4 open questions ARE the rubric — we answer all four

| Cloudflare's question | Our feature | How |
|---|---|---|
| "How do agents know what others are working on?" | **Awareness layer** | Each agent registers its *intent* in the orchestrator before coding; siblings read the shared blackboard to avoid redundant work. |
| "What happens when they make conflicting changes?" | **Hybrid merge engine** (the star) | Real `git` 3-way merge auto-resolves non-overlaps; only true conflict hunks escalate to Claude with full context; result re-verified by tests. |
| "How do you review everything they produce?" | **Event-driven verification** | push event → Queue → **Workflow** runs real tests + typecheck in a **Sandbox**. (Cloudflare's own `REVIEW_WORKFLOW` example pattern.) |
| "How do you track not just what changed, but why?" | **Provenance trail** | Every commit carries an intent record; every merge writes `.braid/trail.json` with conflict, both candidates, resolution, rationale, and test result. |

---

## 2. Hard rule: verification is REAL, not vibes

- **The acceptance gate = running the project's actual tests + typecheck in a real Cloudflare
  Sandbox/Container.** Pass/fail is deterministic. No LLM opinion decides correctness.
- **LLM used in exactly two surgical places:** (a) *generating* each agent's code; (b) *reconciling
  a true conflict hunk* git couldn't auto-merge — and its output is **re-verified by re-running
  tests**. Break tests → reject + retry with the failure fed back.
- **LLM-as-judge appears ONLY in the writeup** as an offline metric to *report* merge quality
  (calibrated-judge method), never as a runtime decision-maker.

This is the difference between "we asked an AI if the code was good" (losing) and "every change is
proven green by execution; the AI only does semantic reconciliation" (winning).

---

## 3. Architecture

```
 you ──task──▶  ORCHESTRATOR  (Durable Object)  ◀── WebSocket ── live dashboard
                 │  holds: task, agents, intents (awareness), state machine
                 │
    env.ARTIFACTS.fork() × N   (cheap isolated repos — Artifacts' core strength)
        ├── fork: agent-1 ──┐
        ├── fork: agent-2 ──┤ each agent: Claude (via AI Gateway) writes code,
        └── fork: agent-N ──┘ isomorphic-git commits + pushes  (write token)
                 │
        push event ──▶ QUEUE ──▶ WORKFLOW  (Cloudflare's own pattern)
                 │                  └─ spins a SANDBOX: install, typecheck, run tests
                 │                     → writes real pass/fail + score to the DO
                 │
        when all green ──▶  INTEGRATOR  (one Sandbox)
                 │   clone each fork's branch; `git merge` them iteratively
                 │   • non-overlapping hunks → git auto-resolves (free, deterministic)
                 │   • true conflict hunk → Claude reconciles WITH full context (Rover-style)
                 │   • re-run tests after each merge → reject+retry if red
                 │   write .braid/trail.json (the "why"); push to main
                 │
        push to main ──▶ Workers Builds ──▶ live PREVIEW URL  (the finale)
```

**Why this split is correct**
- `env.ARTIFACTS` is read + fork + token-mint (no merge/diff), so **orchestration lives in
  Workers/DO** and **real git work lives in Sandboxes** — each primitive to its strength.
- Forking per agent is the *intended* usage (~50k new repos/day reported), so lean into it.
- push events → Queue → Workflow is the exact pattern in Cloudflare's blog → signals "built the
  way Cloudflare intended."

### Components
| Component | Job | Tech |
|---|---|---|
| Orchestrator | task state, agent tracking, intents/awareness, drives sequence, feeds dashboard | Durable Object + WebSocket |
| Control plane | receive task, create forks, mint tokens, API | Worker + `env.ARTIFACTS` |
| Agent worker | per fork: Claude generates code → commit + push | Worker + Claude (AI Gateway) + isomorphic-git |
| Event listener | hears `cf.artifacts.repo.pushed` → notify DO | Artifacts events → Queue → Worker |
| Review Workflow | real tests + typecheck per push, score to DO | Cloudflare Workflows + Sandbox |
| Integrator | iterative `git merge`, conflict → Claude, re-test, push main, write trail | Sandbox + git + Claude |
| Dashboard | live swarm, diverging branches, conflict resolving, trail panel | React + WS |
| Deploy | merged main → running app + URL | Workers Builds |

### Data model (DO state)
```ts
Task  { id, prompt, baselineRepo, status, createdAt }
Agent { id, forkRepo, intent, status: idle|coding|pushed|verified|merged|rejected,
        commitSha, filesTouched[], testResult, score, log[] }
Conflict { path, base, candidates: {agentId, content}[], resolution, rationale, verified }
MergeResult { mergedSha, previewUrl?, conflicts: Conflict[], trail }
```

---

## 4. The merge engine — worked example

Baseline `src/users.ts`:
```ts
export function getUser(id) { return db.find(id); }
```
Agents (intents registered first via awareness layer):
- Agent 1 — validation → edits `getUser()`, adds `validate.ts`
- Agent 2 — rate-limiting → also edits `getUser()`, adds `ratelimit.ts`
- Agent 3 — docs → edits `README.md`

Integrator runs real `git merge`:
- `validate.ts`, `ratelimit.ts`, `README.md` → one agent each → **git auto-merges (no AI).**
- `src/users.ts` → both edited same function → diff3 conflict hunk → **only this hunk** to Claude
  with base + both intents + surrounding context → returns:
```ts
export function getUser(id) {
  if (!id) throw new Error("id required");  // agent-1: validate first
  rateLimit(id);                            // agent-2: then rate-limit
  return db.find(id);
}
```
- **Re-run tests.** Green → commit. Red → reject + retry with failure fed back.

`.braid/trail.json`:
```json
{ "file": "src/users.ts",
  "agents": {"agent-1": "input validation", "agent-2": "rate limiting"},
  "resolution": "kept both; validate before rate-limit",
  "rationale": "independent concerns; validating first avoids spending rate-limit budget on invalid input",
  "verified": "tests passed (12/12)" }
```

Deterministic where possible (Spork/Mergiraf philosophy) + LLM only at true overlap
(Rover/Merge-Bench) + always re-verified by tests.

---

## 5. Demo scenario (lock early — it IS the video)

Small **Hono API** baseline. Task: *"Add input validation and a rate limiter to the /users route."*
Seed so **two agents both edit `src/users.ts`** (guaranteed real conflict) plus each adds a
non-overlapping file (shows the deterministic path). Money shot: dashboard shows two red diverging
branches → AI resolver reconciles the function → both features survive → green merge to `main` →
**Workers Builds preview URL** actually serves the merged app.

### Video beats (~6 min)
1. Problem (30s): GitHub = one author per branch; agents don't work that way.
2. Fire one task → 3 agents fork + code in parallel (live dashboard). *Clears the minimum bar.*
3. The collision: two agents edited the same function → AI merge resolves, side-by-side + rationale.
4. Open `.braid/trail.json` — "here's *why*." (reasoning-tracking box)
5. Click preview URL → merged code is live.
6. Close: "They built a tournament. We built a merge — repos/branches/PRs/merge, reinvented for a thousand concurrent agents."

---

## 6. Day-by-day (Fri 10 → Tue 14)

- **Day 0 (Fri 10-10) — kill the two risks.** Enable Artifacts beta + Claude via AI Gateway.
  Prove (a) isomorphic-git `clone→commit→push` to an Artifacts repo (auth header format unknown);
  (b) a Sandbox can clone a fork w/ write token, `git merge`, `npm test`. Both work → de-risked.
- **Day 1 (Sat 10-11) — swarm + awareness.** DO forks N repos; agents register intent; Claude
  generates; push. Wire push events → Queue → DO. Output: N forks, divergent complementary commits.
- **Day 2 (Sun 10-12) — integrator (hardest).** Iterative `git merge`, conflict-hunk extraction,
  Claude reconciliation w/ context, re-test-and-retry loop, push main, write trail. Whole day.
- **Day 3 (Mon 10-13) — review Workflow + dashboard.** Real test/typecheck Workflow per push;
  live dashboard (swarm, diverging branches, conflict resolving, trail panel). Lock scenario.
- **Day 4 (Tue 10-14) — ship.** Record video, README + run steps, MIT license, submit.
  **Freeze by noon.** Workers Builds preview = only stretch; cut first if needed.

**De-scope ladder (drop in order):** live preview → awareness layer → N=3→N=2 → pre-scripted
single scenario. **Never drop:** real test-verification or the hybrid merge (= the thesis).

---

## 7. Artifacts API facts (grounding)

- Binding `env.ARTIFACTS`: `create`, `get`, `list`, `import`, `delete`; repo handle: `info`,
  `createToken(read|write, ttl)`, `listTokens`, `revokeToken`, `fork`, `log`, `readCommit`,
  `readTree`, `readBlob`, `readFile({ref,path})`.
- **No `merge`, `diff`, branch-list, or PR primitive** — writes go over real Git with a write token.
  *(This gap is the hackathon; we build the merge layer.)*
- Repo = isolated Git service (DO-like, single logical instance, own remote URL + tokens,
  synchronously replicated). Fork = cheap new repo starting from existing history.
- Events: created, imported, forked, deleted, **pushed**, cloned, fetched → Queue → Workers/Workflows.
- Limits: 1 GB/repo · 32 MB/file · 2000 Git req/10s per repo · unlimited repos/namespaces.
- Open beta on Workers Paid; billing starts 2026-10-15.

## 8. Day-0 unknowns to verify
1. isomorphic-git ↔ Artifacts auth: exact token header/URL format for push. **Biggest risk.**
2. Sandbox SDK git + network: can it clone an Artifacts remote and run tests?
3. Event subscription mechanics: dashboard vs API to subscribe to `cf.artifacts.repo.pushed`.

## 9. Repo structure (planned)
```
/worker        control plane (DO orchestrator, queue consumer, API)
/integrator    git merge + conflict detection + Claude resolver + re-test loop
/agent         agent loop (Claude via AI Gateway + isomorphic-git push)
/dashboard     React + WS
/baseline      the demo Hono app we fork
/README.md     run instructions   /LICENSE (MIT)
```

## 10. Research grounding (sources)
- LLM vs. search-based merge (hybrid wins) — arXiv 2605.16646
- Rover: context-aware conflict resolution — arXiv 2605.17279
- Merge-Bench / LLMergeJ — arXiv 2605.25890
- Calibrated LLM-as-judge (offline metric only) — arXiv 2607.27674
- Spork: structured AST merge — arXiv 2202.05329
- LastMerge / Mergiraf: language-agnostic structured merge — arXiv 2507.19687
- SWE-Search (variance → why sample many) — ICLR 2025
- MetaGPT (role-based multi-agent) — ICLR 2024
- Cloudflare blog: https://blog.cloudflare.com/next-git-platform-on-cloudflare/
- Cloudflare docs: https://developers.cloudflare.com/artifacts/

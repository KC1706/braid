# Status — Braid (Cloudflare Git-for-Agents Hackathon)

> Cross-session memory. Update this at the end of every work session: what changed, what's next,
> what's blocked. Newest entry on top. See `plan.md` for the full plan.

**Deadline:** 2026-10-14 · **Today:** 2026-10-10 · **Phase:** Day 2 core done (built offline-first)

## Quick state
- [~] Day 0 — de-risk: isomorphic-git push to Artifacts + Sandbox git/test — BLOCKED on creds
  (no `wrangler login`, no `ANTHROPIC_API_KEY`/`CF_*` in env). Built the cloud-independent core instead.
- [ ] Day 1 — swarm + awareness layer (fork N, agents code & push, events→DO) — needs creds
- [x] Day 2 — integrator (git merge + conflict resolver + re-test loop) — ✅ DONE & TESTED offline
- [ ] Day 3 — review Workflow + live dashboard
- [x] Day 4 (partial) — README + MIT license written

## Locked decisions
- Concept A: swarm + hybrid AI merge (compose, don't pick a winner).
- Claude via Cloudflare AI Gateway for codegen + conflict reconciliation.
- Verification = real tests in a Sandbox (NOT an AI judge).
- Scope = tight vertical slice; name = **Braid** (tentative).

## Open unknowns (verify Day 0)
1. isomorphic-git ↔ Artifacts auth: exact token header/URL format for push. (biggest risk)
2. Sandbox SDK: can it clone an Artifacts remote + run tests?
3. Event subscription: dashboard vs API for `cf.artifacts.repo.pushed`.

## Environment checklist
- [ ] Cloudflare Workers Paid account
- [ ] Artifacts beta enabled
- [ ] Anthropic API key + AI Gateway route configured
- [ ] wrangler installed / logged in

---

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

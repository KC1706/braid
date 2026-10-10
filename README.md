# Braid — Git for a thousand concurrent agents

> Many AI agents work on **one** codebase at once, each in its own cheap fork. A hybrid merge
> engine **composes** their complementary work into a single codebase — auto-resolving
> non-overlaps with real `git`, reconciling true conflicts with an LLM, and proving every change
> green by running the project's **actual tests**.
>
> They built a tournament. We built a merge.

Cloudflare "next Git platform for the agentic era" hackathon entry. MIT licensed.

## The thesis

Most multi-agent systems run best-of-N: spawn N agents, keep the single best, discard the rest.
That wastes N−1 agents and never solves *concurrent changes to one repo* — the literal thing the
prompt asks for. Braid's contrarian bet is **composition, not selection**: N agents do
complementary pieces of one task, and Braid merges their work, reconciling collisions.

Verification is **real, not vibes**: the acceptance gate is the project's own test suite running in
a sandbox. An LLM is used in exactly two surgical places — (a) generating each agent's code, and
(b) reconciling a true conflict hunk `git` couldn't auto-merge — and its merge output is always
**re-verified by re-running the tests**. No LLM opinion ever decides correctness.

## What runs today (no cloud needed)

The heart of the project — the test-verified hybrid merge engine — is fully built and runs locally
on real `git` + real tests. The production integrator runs the *same* `git` inside a Cloudflare
Sandbox, so this is the real code path, not a mock.

```bash
npm install
npm run demo      # seeds a baseline + 3 agent forks, then integrates them
```

The demo reproduces the worked example: three agents fork one Hono API —

- **agent-1** adds input validation (edits `getUser()`, adds `validate.ts`)
- **agent-2** adds rate limiting (**also** edits `getUser()`, adds `ratelimit.ts`)
- **agent-3** updates the docs (edits `README.md`)

Braid integrates them:

- `validate.ts`, `ratelimit.ts`, `README.md` → one agent each → **git auto-merges, no AI**
- `src/users.ts` → both agents edited the same function → **true conflict** → resolver composes
  both → **tests re-run green** → committed
- `.braid/trail.json` records the *why*: both candidates, the resolution, the rationale, the test
  result.

Result: one green `src/users.ts` where **both features survive**:

```ts
export function getUser(id: string): User | undefined {
  validateId(id);     // agent-1: validate first
  rateLimit(id);      // agent-2: then rate-limit
  return db.find(id);
}
```

```bash
npm test          # baseline suite + integrator unit + end-to-end merge tests
```

## The resolver is hybrid and pluggable

- `UnionResolver` — deterministic 3-way union (Spork/Mergiraf philosophy): keeps each agent's
  additions around the shared base lines. No network, no nondeterminism. This is the offline demo
  path and the first attempt for every conflict.
- `ClaudeResolver` — calls Claude via Cloudflare AI Gateway (`CF_AI_GATEWAY`) or the Anthropic API
  directly. Used for general conflicts the cheap path can't satisfy.
- `HybridResolver` — union first; escalates to Claude only on a retry (i.e. when the deterministic
  resolution broke the tests). Set `ANTHROPIC_API_KEY` (and optionally `CF_AI_GATEWAY`) to enable.

Every resolution — union or LLM — is gated by the real test suite before it's committed. Red →
re-resolve with the failure fed back, bounded retries.

## Architecture

```
 task ─▶ ORCHESTRATOR (Durable Object) ◀─ WebSocket ─ live dashboard
          │  holds task, agents, intents (awareness), state machine
   env.ARTIFACTS.fork() × N   (cheap isolated repos)
          ├─ agent-k: Claude (via AI Gateway) writes code → isomorphic-git push
          │
   push event ─▶ QUEUE ─▶ WORKFLOW → spins a SANDBOX: install, typecheck, test → pass/fail to DO
          │
   all green ─▶ INTEGRATOR (one Sandbox): iterative git merge
          │   • non-overlap → git auto-resolves    • true conflict → Claude, re-tested
          │   • write .braid/trail.json             • push to main
          │
   push to main ─▶ Workers Builds ─▶ live preview URL
```

| Component | Status |
|---|---|
| `baseline/` — Hono `/users` API + real vitest suite (the fork target & test gate) | ✅ done |
| `integrator/` — hybrid merge engine: git 3-way + resolver + re-test loop + trail | ✅ done, tested |
| `worker/` — control plane: DO orchestrator, forks, queue consumer, WS | ⏳ scaffold next (needs Cloudflare creds) |
| `agent/` — per-fork Claude codegen + isomorphic-git push | ⏳ next (needs AI Gateway + Artifacts) |
| `dashboard/` — live swarm / diverging branches / trail panel | ⏳ next |

## Repo layout

```
baseline/     demo Hono app the swarm forks; its tests are the acceptance gate
integrator/   the hybrid merge engine (git + resolver + verify + trail)  ← the star
  src/        git.ts conflict.ts resolver.ts verify.ts integrate.ts trail.ts
  demo/       seed.ts (fixture) + run.ts (the money-shot demo)
agent/        (planned) agent loop: Claude via AI Gateway → push
worker/       (planned) DO orchestrator + queue + API
dashboard/    (planned) React + WebSocket live view
```

## Running against real Cloudflare (requires credentials)

Not yet wired end-to-end — needs a Workers Paid account with Artifacts beta, an AI Gateway route,
and `wrangler login`. The merge engine above is cloud-independent and already proven; the remaining
work is wiring forks/push/sandbox/deploy around it. See `plan.md` §6 for the day-by-day and the
de-scope ladder.

## License

MIT — see `LICENSE`.

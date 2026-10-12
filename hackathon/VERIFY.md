# Braid — Manual Verification Guide

This walks you through **proving the whole system works with your own eyes**, from the
laptop-only merge engine up to a full swarm running on real Cloudflare Artifacts. Every step says
the exact command, **what you should see**, and **what it proves**.

> Big picture: Braid lets several AI agents each work on their own copy (fork) of one repo, then a
> merge engine stitches their work back together — auto-merging what doesn't clash, resolving what
> does, and **running the real test suite** to prove the result works before accepting it.

**Your account specifics (already set up):**
- Cloudflare account ID: `06bff0b64288392a55e653c32310fc63`
- Artifacts namespace: `braid`
- Deployed control-plane Worker: `https://braid-worker.kunalchandoa925.workers.dev`
- GitHub repo: `KC1706/braid`

---

## 0. Prerequisites (one-time)

```bash
cd /Users/apple/cloudflare_hackathon
node --version          # expect v20+ (v22 is fine)
npx wrangler whoami     # expect: logged in as kunalchandoa925@gmail.com
npm install             # installs all workspace deps
```

**What it proves:** your toolchain is ready and wrangler is authenticated to the paid account.

---

## PART A — The merge engine, on your laptop (no cloud)

This is the core thesis. It runs on plain local `git` + real tests, so you can trust it before any
cloud is involved.

### A1. Run the tests

```bash
npm test
```

**What you should see:** `baseline` suite 2 passed, `integrator` suite 4 passed (including an
end-to-end merge test). No failures.

**What it proves:** the conflict parser, the deterministic union resolver, and the full
merge-and-re-test pipeline all work.

### A2. Run the local demo

```bash
npm -w integrator run demo
```

**What you should see**, in order:
1. It seeds a baseline repo + 3 agent branches (agent-1 validation, agent-2 rate-limit, agent-3 docs).
2. `agent-1` → *git auto-merged 2 files — no AI needed* ✅
3. `agent-2` → *1 file conflicted … resolved via union … Tests 2 passed* ✅ (this is the real clash:
   both agents edited `getUser()`)
4. `agent-3` → *git auto-merged 1 file* ✅
5. The merged `src/users.ts` printed, containing **both** features:
   ```ts
   export function getUser(id: string): User | undefined {
     validateId(id);
     rateLimit(id);
     return db.find(id);
   }
   ```
6. `.braid/trail.json` printed (the "why"), then `status: GREEN ✅`.

**What it proves:** when two agents edit the same function, Braid composes both changes and proves
the result with tests — it does not pick one and throw the other away.

---

## PART B — The Cloudflare control plane

### B1. (Re)deploy the Worker

```bash
cd worker && npx wrangler deploy && cd ..
```

**What you should see:** it lists two bindings — `env.ARTIFACTS (braid)` and `env.AI` — and prints
the deployed URL.

**What it proves:** the control plane is live with access to Artifacts (git repos) and Workers AI
(the on-Cloudflare LLM).

### B2. Create a repo + forks (the swarm's substrate)

```bash
# 1) create a base repo (returns a name + write token)
curl -s -X POST https://braid-worker.kunalchandoa925.workers.dev/api/base -d '{}' | tee /tmp/base.json

# 2) fork it 3 times, one per agent (use the "name" from step 1)
BASE=$(python3 -c 'import json;print(json.load(open("/tmp/base.json"))["name"])')
curl -s -X POST https://braid-worker.kunalchandoa925.workers.dev/api/fork \
  -H 'content-type: application/json' \
  -d "{\"base\":\"$BASE\",\"agents\":[\"agent-1\",\"agent-2\",\"agent-3\"]}" | python3 -m json.tool
```

**What you should see:** step 1 returns `{name, remote, token, …}`; step 2 returns 3 forks, each with
its own `remote` and `token`.

**What it proves:** the Worker can create and fork real Artifacts repos and mint per-fork write
tokens on demand — the "give every agent its own cheap copy" primitive.

### B3. Test the Workers AI conflict resolver (no Anthropic key)

```bash
curl -s -X POST https://braid-worker.kunalchandoa925.workers.dev/api/resolve \
 -H 'content-type: application/json' \
 -d '{
   "path":"src/users.ts",
   "oursIntent":"input validation",
   "theirsIntent":"rate limiting",
   "base":"  return db.find(id);",
   "ours":"  validateId(id);\n  return db.find(id);",
   "theirs":"  rateLimit(id);\n  return db.find(id);",
   "contextBefore":"export function getUser(id: string): User | undefined {",
   "contextAfter":"}"
 }' | python3 -m json.tool
```

**What you should see:** a JSON response with `model` set to a Llama model and `content` equal to:
```
validateId(id);
  rateLimit(id);
  return db.find(id);
```

**What it proves:** Braid can reconcile a true conflict with an LLM that runs **entirely on
Cloudflare** (Workers AI), no third-party API key required.

---

## PART C — The full swarm on real Artifacts (the headline)

### C1. Run it

```bash
npm -w integrator run swarm:live
```

**What you should see** (takes ~20–40s):
1. `① Creating a real Artifacts base repo …` → prints a base repo name like `braid-live-xxxx`.
2. `② Seeding … baseline pushed`.
3. `③ Forking … agent-1/2/3`.
4. `④ Each agent edits its fork and pushes` (3 ✓ lines).
5. `⑤ Integrator: clone base, fetch every fork, merge + re-test` — same pattern as the local demo:
   agent-1 auto-merge ✅, agent-2 conflict resolved + Tests 2 passed ✅, agent-3 auto-merge ✅.
6. `⑥ Pushing the merged … result back to base/main`.
7. `⑦` prints the merged `src/users.ts` (both features), `⑧ status: GREEN ✅`.
8. The last line prints the base repo's clone URL. **Copy the repo name** (`braid-live-xxxx`) for C2.

**What it proves:** the end-to-end story works on real Cloudflare infrastructure — not a mock.

### C2. Independently confirm the result landed in Cloudflare

Don't trust the script's own word — clone the repo fresh yourself.

```bash
# Use the base repo NAME printed at the end of C1:
REPO=braid-live-xxxx            # <-- replace with the real name

# mint a short-lived read token for it
cd worker
TOKEN=$(npx wrangler artifacts repos issue-token "$REPO" --namespace braid --scope read --ttl 600 --json \
        | python3 -c 'import sys,json;print(json.load(sys.stdin)["plaintext"])')
cd ..

REMOTE="https://06bff0b64288392a55e653c32310fc63.artifacts.cloudflare.net/git/braid/$REPO.git"
rm -rf /tmp/verify-braid
git -c http.extraHeader="Authorization: Bearer $TOKEN" clone "$REMOTE" /tmp/verify-braid

echo "--- files ---"; (cd /tmp/verify-braid && git ls-files)
echo "--- merged users.ts ---"; cat /tmp/verify-braid/src/users.ts
echo "--- provenance trail ---"; cat /tmp/verify-braid/.braid/trail.json
echo "--- history ---"; (cd /tmp/verify-braid && git log --oneline)
```

**What you should see:**
- Files include `src/users.ts`, `src/validate.ts`, `src/ratelimit.ts`, `README.md`, `.braid/trail.json`.
- `users.ts` has **both** `validateId(id);` and `rateLimit(id);`.
- `trail.json` shows `agentsMerged: 3`, `conflictsResolved: 2`, `finalTest: Tests 2 passed`.
- History shows 3 agent commits → 2 merge commits → a `braid: provenance trail` commit.

**What it proves:** the merged, test-verified result and its full provenance genuinely live in
Cloudflare Artifacts, reconstructable by anyone with a token.

---

## PART D — See your repos in Cloudflare

```bash
cd worker && npx wrangler artifacts repos list --namespace braid --json | python3 -m json.tool | head -40; cd ..
```

**What you should see:** a list including your `braid-live-*` base and `*-agent-N` forks.

---

## Cleanup (optional)

Test runs create repos. They're tiny and free until 2026-10-14, but to tidy up:

```bash
cd worker
# delete one repo
npx wrangler artifacts repos delete <repo-name> --namespace braid
# (repeat for the agent forks: <base>-agent-1, -agent-2, -agent-3)
cd ..
```

---

## Troubleshooting

| Symptom | Cause / fix |
|---|---|
| `Access denied [code: 10004]` | Account not on Workers Paid, or wrong account. `npx wrangler whoami`. |
| `Authentication failed` on clone | Token expired (TTL) or wrong scope. Re-issue with `issue-token … --scope read`. |
| `issue-token` prints no token | The field is `plaintext` (not `token`). |
| Swarm hangs on test step | The merged tree needs deps; the script symlinks the repo-root `node_modules`. Run `npm install` at the repo root first. |
| Want the LLM to actually fire | The hybrid resolver uses the fast deterministic union first; Workers AI only triggers on a retry (when union fails tests). B3 tests the LLM path directly. |

---

## What this does and doesn't yet cover

**Proven here:** the merge engine, the Artifacts create/fork/token/push substrate, Workers AI
conflict resolution, and the full swarm end-to-end on real Artifacts.

**Not yet built (demo polish, see `status.md`):** the live web dashboard + orchestrator Durable
Object, moving agent/integrator compute fully inside Workers/Sandbox, and push→Queue→Workflow
auto-review.

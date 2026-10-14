# Braid

**A hybrid, test-verified merge engine for concurrent AI agents — running on Cloudflare.**

When a swarm of AI agents edits the same repository at once, they don't conflict in a
chat window — they conflict in the code. Braid gives each agent its own fork, then merges
them on a shared base using **git's own 3-way merge for the clean cases and AI only for the
genuine conflicts** — and it **refuses to publish anything that doesn't pass the tests**.
The result is a single green commit from many parallel agents, with a **tamper-evident
record** of exactly how it got there.

```
agents' forks ──▶ [IO zone: snapshot]  ──▶ [Compute zone: merge + run tests]  ──▶ [IO zone: CAS-publish]
                   (holds tokens)            (no token, no egress)                 (git --force-with-lease)
                                                      │
                                                      └─▶ signed Ed25519 ledger (R2) + rebuildable event log (DO)
```

---

## Architecture at a glance

- **Two security zones.** Untrusted agent code is merged and tested in a **compute zone with
  no credentials and default-deny egress**; the single base write-token lives only in an
  **I/O zone that never executes agent code**. The merged work crosses the boundary as a
  content-addressed git bundle.
- **No lost work, no duplicates.** The base advances via a **compare-and-swap on the parent
  SHA** (a per-base coordinator + git `--force-with-lease`). Two agents racing the same base
  can't clobber each other — the loser automatically **rebases and retries**.
- **Tamper-evident.** Every decision is appended to an **Ed25519-signed SHA-256 hash chain**
  in R2. Anyone can verify it offline with just the public key.
- **Rebuildable.** Each run's state is an append-only, seq-numbered **event log** in a Durable
  Object; the live projection always equals a replay of the log.

Full design: [`ARCHITECTURE_PRODUCTION.md`](./ARCHITECTURE_PRODUCTION.md) ·
Roadmap + exit gates: [`GO_PRODUCTION_PLAN.md`](./GO_PRODUCTION_PLAN.md).

## How Cloudflare is used

| Primitive | Role |
|---|---|
| **Workers** | Control plane (`/v1` API, auth + RBAC) and orchestration |
| **Workflows** | Durably drives each run: snapshot → merge → verify → CAS-publish (+ auto-rebase) |
| **Containers** | The two security zones (compute: no secrets, no internet; I/O: holds the write token) |
| **Durable Objects** | Base-Coordinator (CAS lease) + rebuildable Orchestrator (event log) |
| **R2** | The signed provenance ledger + large event-body offload |
| **Workers AI** | Resolves genuine merge conflicts (tests still decide correctness) |
| **Artifacts** | Git repos: the base, the per-agent forks, and the merged bundle |

---

## Repository layout

```
cmd/braid/       Go CLI (serve the zones, ledger keygen/verify)
internal/        Go engine: merge, conflict ladder, resolver, gitio, ledger, serve, …
pkg/             Shared Go packages (gitx, models)
worker/          Cloudflare Worker: /v1 control plane, Workflow, zones, DOs (TypeScript)
dashboard/       Operator console (static page served by the Worker)
Dockerfile       Multi-stage image for the two Container zones (one image, zone by env)
hackathon/       Frozen original hackathon submission (kept for provenance; not built)
```

---

## Run it

### Prerequisites
- **Go 1.26+**
- **Node 20+**
- **Docker Desktop** running (Wrangler builds the Container images)
- A **Cloudflare account** with Workers Paid (Containers + Durable Objects + R2 + Workers AI)
- **Wrangler** (`npm install` pulls it into the worker workspace)

### 1. Run the test suite (no Cloudflare account needed)

```bash
# Go engine — pure, deterministic, offline
go test ./...

# Worker — pure control-plane / reducer / ledger unit tests
cd worker && npm install && npm test
```

Both suites are green and run with no network or credentials.

### 2. Deploy to Cloudflare

```bash
cd worker
npm install
npx wrangler login                 # one-time, in your own shell

# Two secrets (set them yourself — never commit these):
#   CONTROL_TOKENS      JSON map: { "<bearer-token>": { "id": "...", "roles": ["admin"|"operator"|"viewer"] }, ... }
#   LEDGER_SIGNING_SEED hex-encoded Ed25519 seed (generate with: go run ../cmd/braid ledger keygen)
npx wrangler secret put CONTROL_TOKENS
npx wrangler secret put LEDGER_SIGNING_SEED

npx wrangler deploy                # builds the Container images (Docker must be running)
```

`wrangler deploy` provisions the Workflow, the four Durable Objects, the R2 bucket, the
Artifacts namespace, the Workers AI binding, and the two Container zones defined in
[`worker/wrangler.jsonc`](./worker/wrangler.jsonc).

### 3. Use it

**Operator console** — open `https://<your-worker>.workers.dev/console`, paste an operator
bearer token, and connect. You can start runs, watch the live event log, review the signed
ledger + rebuild proof, and see the per-run cost/margin.

**API** (all of `/v1/*` requires a `Bearer` token; it fails closed):

```bash
B=https://<your-worker>.workers.dev
TOKEN=<your-operator-token>

# Start a run: fork the base per agent, mint scoped tokens, enqueue the two-zone Workflow
curl -X POST "$B/v1/runs" -H "authorization: Bearer $TOKEN" -H "content-type: application/json" \
  -d '{"base":"<artifacts-base-repo>","agents":["agent-1","agent-2"],"task":"harden the API"}'

curl -H "authorization: Bearer $TOKEN" "$B/v1/runs"               # list runs
curl -H "authorization: Bearer $TOKEN" "$B/v1/runs/<id>/live"     # live projection
curl -H "authorization: Bearer $TOKEN" "$B/v1/runs/<id>/ledger"   # signed provenance chain
curl -H "authorization: Bearer $TOKEN" "$B/v1/runs/<id>/cost"     # per-run $/run + margin
```

**Verify the ledger offline** — tamper-evidence with only the public key:

```bash
curl -H "authorization: Bearer $TOKEN" "$B/v1/runs/<id>/ledger" > chain.json
go run ./cmd/braid ledger verify -pubkey <public-key-hex> -file chain.json
# → "chain intact and every signature valid"  (alter any record and it reports TAMPER DETECTED)
```

---

## License

MIT — see [`LICENSE`](./LICENSE).

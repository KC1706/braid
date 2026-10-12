# Braid architecture (Go rewrite in progress)

Braid is being rewritten from the TypeScript hackathon demo into a single-language **Go** platform,
with the author's own **Keploy** and **Entire** code migrated **in-tree** (this repo is the home — no
external/upstream dependency on those projects). The frozen TS submission lives in [`hackathon/`](./hackathon/)
and must not be modified.

Unifying idea — three git-native layers compose:

| Layer | "Git for…" | Role |
|---|---|---|
| Cloudflare Artifacts | code | storage (one repo/Durable Object per fork) |
| Keploy (→ `pkg/verify`) | behavior | record/replay + test-gen → the acceptance gate |
| Entire (→ `pkg/provenance`) | intent | why each change exists, attributed → provenance + memory |

Braid is the engine that **composes code, proves it against behavior, and records why.**

## Target module layout — `github.com/KC1706/braid` (Go 1.26)

```
cmd/braid/            # CLI + sandbox entrypoint (the integrator binary)
internal/             # Braid's novel IP (private)
  merge/              # git 3-way merge driver        (port of integrator git.ts+integrate.ts)
  ladder/             # verification ladder + Verdict  (port of ladder.ts)
  resolver/           # union + hybrid + LLM resolver  (port of resolver.ts)
  semantic/           # semantic-conflict graph        (port of semantic.ts → tree-sitter, multi-lang)
  policy/             # incompatibility policy          (port of policy.ts)
pkg/                  # public, contributor-facing
  gitx/               # git plumbing wrappers           [DONE]
  models/             # shared types
  verify/             # MIGRATED from Keploy: coverage, testgen, replay, contract
  provenance/         # MIGRATED from Entire: checkpoint, redact, attribution, memory
edge/                 # thin TS shim (the one non-Go corner): Worker + Orchestrator DO
  worker/             #   front door, WebSocket, env.ARTIFACTS binding (Workers don't run native Go)
  dashboard/          #   static HTML (language-neutral)
sandbox/              # container image: braid binary + language runtimes for verification
hackathon/            # FROZEN TS submission snapshot — do not modify
```

**Why the edge stays TS:** Cloudflare Workers / Durable Objects run on V8 (JS/WASM), not native Go.
The Worker is a thin shim (routing, WebSocket, the Artifacts binding, DO state) fronting a fat Go core
that runs in a Container/Sandbox. Thin edge, fat core.

## Migration status (strangler, not big-bang)

- [x] Freeze the hackathon snapshot (`hackathon/`)
- [x] Go module skeleton + CI (`.github/workflows/go.yml`)
- [x] `pkg/gitx` — git plumbing (ported + tested)
- [ ] `internal/merge`, `internal/ladder`, `internal/resolver`, `internal/policy` — port the brain, re-create the 24 TS tests in Go
- [ ] `internal/semantic` — tree-sitter multi-language symbol graph
- [ ] `pkg/verify` — migrate Keploy (sever Mongo `ReportDB`/zap/ai-service → small Braid interfaces); pull coverage forward to de-risk
- [ ] `pkg/provenance` — migrate Entire (checkpoints-as-git-refs, redaction, attribution)
- [ ] rebuild the thin TS edge against the Go core; delete old TS

The correctness design (verification ladder, the C1–C22 critique and fixes) is in
[`PRODUCTION_PLAN.md`](./PRODUCTION_PLAN.md). Session handoff is in [`status.md`](./status.md).

## Build

```bash
go build ./...
go test ./... -count=1
```

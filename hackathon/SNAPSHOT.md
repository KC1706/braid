# Braid — Hackathon Submission (frozen snapshot)

This folder is a **frozen, self-contained copy** of the Braid hackathon submission: the TypeScript
swarm → hybrid, test-verified merge engine + the live dashboard, exactly as proven end-to-end on real
Cloudflare Artifacts.

> **Do not modify this folder.** The project's production rewrite (Go) happens at the repo root. This
> snapshot exists so the submission stays stable and recordable while the production build evolves.
> Frozen on 2026-10-12 (through commit of the Phase-1 correctness work: verification ladder,
> structural guards, semantic-conflict detect+repair, incompatibility path).

## Run it standalone

```bash
cd hackathon
npm install
npm test                       # baseline 2 + integrator 24
BRAID_DASHBOARD=off npm run demo   # local demo (no network)
npm run demo                   # streams to the live dashboard
```

## The money shot

- **Live dashboard:** https://braid-worker.kunalchandoa925.workers.dev/
- Local demo prints a `…/?run=<id>` URL; open it to watch the swarm stream live.
- Full swarm on real Artifacts: `npm -w integrator run swarm:live`

See this folder's `README.md`, `VERIFY.md`, and `plan.md` for the full writeup. The forward-looking
production design is in the repo root's `PRODUCTION_PLAN.md`.

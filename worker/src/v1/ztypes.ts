/**
 * Pure wire types + client interfaces for Braid's two compute zones, mirroring
 * the Go `internal/serve` HTTP API. This module imports NOTHING from the Workers
 * runtime, so the orchestration (integration.ts) and its unit tests can depend on
 * it in plain node. The runtime adapter that actually talks to a Container lives
 * in zones.ts and imports these.
 *
 * The security split is in WHO gets a token: io-zone requests (snapshot/publish)
 * carry Artifacts tokens; the compute-zone request (integrate) has no token field
 * at all.
 */

/** A scoped Artifacts credential on the wire (io zone only). */
export interface TokenJSON {
  plaintext: string;
  expiresAt?: string; // RFC3339; omitted = never
}

export interface RepoRef {
  remote: string;
  branch: string;
  token: TokenJSON;
}

export interface AgentRefJSON {
  id: string;
  remote?: string; // present on snapshot (io); omitted on integrate (compute)
  branch?: string;
  intent?: string;
  token?: TokenJSON; // io only
}

export interface SnapshotRequest {
  base: RepoRef;
  agents: AgentRefJSON[];
}
export interface SnapshotResponse {
  bundle: string; // base64 prepared bundle
  baselineSha: string;
  branches: Record<string, string>;
}

/** No token field exists here by design — the compute zone never sees one. */
export interface IntegrateRequest {
  bundle: string;
  agents: { id: string; intent?: string }[];
  task?: string;
  resolver?: string;
  integrationBranch?: string;
  tree?: boolean;
  vet?: boolean;
  maxRetries?: number;
}
export interface IntegrateResponse {
  verdict: string;
  green: boolean;
  baseSha: string;
  mergedSha: string;
  ref: string;
  mergedBundle?: string; // present only when green
  summary: Record<string, number>;
  // (the compute zone also returns a full `trail`; the Worker does not need it,
  // so it is intentionally not modeled on the wire type here.)
}

export interface PublishRequest {
  base: RepoRef;
  mergedBundle: string;
  baseSha: string;
  mergedSha: string;
  ref: string;
}
export interface PublishResponse {
  pushedSha: string;
}

/** Raised by the io client when a CAS publish is rejected (base moved, HTTP 409). */
export class BaseMovedError extends Error {
  constructor() {
    super("gitio: base tip moved since the merge was computed (CAS failed)");
    this.name = "BaseMovedError";
  }
}

/** The io zone: holds tokens, clones/CAS-pushes. */
export interface IOZoneClient {
  snapshot(req: SnapshotRequest): Promise<SnapshotResponse>;
  publish(req: PublishRequest): Promise<PublishResponse>; // throws BaseMovedError on 409
}

/** The compute zone: merges + runs tests. No token. */
export interface ComputeZoneClient {
  integrate(req: IntegrateRequest): Promise<IntegrateResponse>;
}

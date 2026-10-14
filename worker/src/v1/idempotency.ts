/**
 * Idempotency for POST /v1/runs (GO_PRODUCTION_PLAN.md §3 Phase 3b). A client
 * that retries a create (network blip, at-least-once delivery) must not fork the
 * base twice or double-charge. The client sends an Idempotency-Key; the first
 * request under a key runs the work and stores its result, and a replay returns
 * that stored result verbatim. A replay of the SAME key with a DIFFERENT body is
 * a client error (409) — the key was reused for a different request.
 *
 * The store is an interface (a DO's storage in prod, a Map in tests). The
 * fingerprint is a stable hash of the request body so key-reuse is detectable
 * without storing the raw body.
 */

const enc = new TextEncoder();

/** A stored idempotency record: the body fingerprint and the response to replay. */
export interface IdempotencyRecord {
  fingerprint: string;
  status: number;
  body: string; // serialized response body
}

export interface IdempotencyStore {
  get(key: string): Promise<IdempotencyRecord | null>;
  put(key: string, record: IdempotencyRecord): Promise<void>;
}

export class MapIdempotencyStore implements IdempotencyStore {
  private readonly m = new Map<string, IdempotencyRecord>();
  async get(key: string): Promise<IdempotencyRecord | null> {
    return this.m.get(key) ?? null;
  }
  async put(key: string, record: IdempotencyRecord): Promise<void> {
    this.m.set(key, record);
  }
}

/** fingerprint is a stable hex SHA-256 of the request body. */
export async function fingerprint(body: string): Promise<string> {
  const digest = await crypto.subtle.digest("SHA-256", enc.encode(body));
  const bytes = new Uint8Array(digest);
  let out = "";
  for (const b of bytes) out += b.toString(16).padStart(2, "0");
  return out;
}

export type IdempotencyOutcome =
  | { kind: "replay"; status: number; body: string } // stored result, return it
  | { kind: "conflict"; error: string } // key reused with a different body -> 409
  | { kind: "proceed"; commit: (status: number, body: string) => Promise<void> }; // first time

/**
 * begin looks up the key. If seen with the same fingerprint, it returns the
 * stored result to replay. If seen with a different fingerprint, a conflict. If
 * unseen, it returns a `commit` the handler calls with its response to store it.
 * When no key is supplied, the handler should proceed without dedup — callers
 * pass `null` and get a no-op `proceed`.
 */
export async function begin(
  store: IdempotencyStore,
  key: string | null,
  body: string,
): Promise<IdempotencyOutcome> {
  if (!key) {
    return { kind: "proceed", commit: async () => {} };
  }
  const fp = await fingerprint(body);
  const existing = await store.get(key);
  if (existing) {
    if (existing.fingerprint !== fp) {
      return { kind: "conflict", error: "Idempotency-Key reused with a different request body" };
    }
    return { kind: "replay", status: existing.status, body: existing.body };
  }
  return {
    kind: "proceed",
    commit: async (status: number, respBody: string) => {
      await store.put(key, { fingerprint: fp, status, body: respBody });
    },
  };
}

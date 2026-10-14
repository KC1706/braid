/**
 * The Worker's writer for Braid's signed provenance ledger (GO_PRODUCTION_PLAN.md
 * §3 Phase 4b, C17/C18). It appends one signed record per run step to a Store
 * (R2 in prod) as a run executes. The record format is byte-for-byte identical to
 * the Go `internal/ledger` package, so `braid ledger verify` validates the chain
 * offline with only the public key — the whole point of signing it.
 *
 * Interop is locked two ways in ledger.test.ts: the canonical hash matches a Go
 * vector, and an Ed25519 signature (deterministic) matches a Go-produced one.
 */

const GENESIS_PREV = "GENESIS";
const enc = new TextEncoder();

/** A ledger record — same shape + JSON tags as ledger.Record in Go. */
export interface Record {
  seq: number;
  prevHash: string;
  runId: string;
  kind: string;
  at: string;
  payload: Record_Payload;
  hash: string;
  signature: string;
}
type Record_Payload = { [k: string]: string };

function toHex(buf: ArrayBuffer): string {
  const b = new Uint8Array(buf);
  let s = "";
  for (const x of b) s += x.toString(16).padStart(2, "0");
  return s;
}

/**
 * contentHash replicates Go determinism.ContentHash: hex SHA-256 over the parts,
 * each prefixed with its BYTE length (decimal) + ":" so part boundaries are
 * unambiguous. Byte length (not JS string length) matters for non-ASCII.
 */
export async function contentHash(parts: string[]): Promise<string> {
  const chunks: Uint8Array[] = [];
  for (const p of parts) {
    const pb = enc.encode(p);
    chunks.push(enc.encode(String(pb.length) + ":"));
    chunks.push(pb);
  }
  const total = chunks.reduce((n, c) => n + c.length, 0);
  const buf = new Uint8Array(total);
  let off = 0;
  for (const c of chunks) {
    buf.set(c, off);
    off += c.length;
  }
  return toHex(await crypto.subtle.digest("SHA-256", buf));
}

/**
 * canonicalOrder replicates Go determinism.CanonicalOrder(keys, key=>key):
 * ascending by contentHash(key), with the key itself as the tiebreaker. Hashes
 * are precomputed so the sort is synchronous.
 */
async function canonicalOrder(keys: string[]): Promise<string[]> {
  const withHash = await Promise.all(keys.map(async (k) => ({ k, h: await contentHash([k]) })));
  withHash.sort((a, b) => (a.h < b.h ? -1 : a.h > b.h ? 1 : a.k < b.k ? -1 : a.k > b.k ? 1 : 0));
  return withHash.map((x) => x.k);
}

/** canonicalHash replicates Go ledger.CanonicalHash exactly. */
export async function canonicalHash(
  seq: number,
  prevHash: string,
  runId: string,
  kind: string,
  at: string,
  payload: Record_Payload,
): Promise<string> {
  const parts = [String(seq), prevHash, runId, kind, at];
  for (const k of await canonicalOrder(Object.keys(payload))) {
    parts.push(k, payload[k]);
  }
  return contentHash(parts);
}

// PKCS8 DER prefix for an Ed25519 private key wrapping a 32-byte seed. WebCrypto
// imports Ed25519 private keys as pkcs8, so we wrap the raw seed with this fixed
// header (RFC 8410): SEQ{ INTEGER 0, SEQ{ OID 1.3.101.112 }, OCTET STRING{ OCTET STRING seed } }.
const PKCS8_ED25519_PREFIX = new Uint8Array([
  0x30, 0x2e, 0x02, 0x01, 0x00, 0x30, 0x05, 0x06, 0x03, 0x2b, 0x65, 0x70, 0x04, 0x22, 0x04, 0x20,
]);

function hexToBytes(hex: string): Uint8Array {
  const out = new Uint8Array(hex.length / 2);
  for (let i = 0; i < out.length; i++) out[i] = parseInt(hex.slice(i * 2, i * 2 + 2), 16);
  return out;
}

function base64(buf: ArrayBuffer): string {
  const b = new Uint8Array(buf);
  let s = "";
  for (const x of b) s += String.fromCharCode(x);
  return btoa(s);
}

/** Signer signs a record hash (the hex string) and returns a base64 signature. */
export interface Signer {
  sign(hash: string): Promise<string>;
}

/**
 * Ed25519Signer imports a 32-byte seed (hex) as an Ed25519 private key and signs
 * the UTF-8 bytes of the hash hex string — exactly what Go's ed25519.Sign([]byte(hash))
 * does, so Go's ed25519.Verify under the matching public key accepts the signature.
 */
export class Ed25519Signer implements Signer {
  private keyPromise: Promise<CryptoKey>;
  constructor(seedHex: string) {
    const seed = hexToBytes(seedHex);
    if (seed.length !== 32) throw new Error(`ledger: seed must be 32 bytes, got ${seed.length}`);
    const pkcs8 = new Uint8Array(PKCS8_ED25519_PREFIX.length + 32);
    pkcs8.set(PKCS8_ED25519_PREFIX, 0);
    pkcs8.set(seed, PKCS8_ED25519_PREFIX.length);
    this.keyPromise = crypto.subtle.importKey("pkcs8", pkcs8, { name: "Ed25519" }, false, ["sign"]);
  }
  async sign(hash: string): Promise<string> {
    const key = await this.keyPromise;
    const sig = await crypto.subtle.sign({ name: "Ed25519" }, key, enc.encode(hash));
    return base64(sig);
  }
}

/** LedgerStore persists one run's chain (R2 in prod; an in-memory fake in tests). */
export interface LedgerStore {
  list(runId: string): Promise<Record[]>;
  append(runId: string, record: Record): Promise<void>;
}

/** The subset of an R2 bucket the ledger store uses. */
export interface R2Like {
  put(key: string, value: string): Promise<unknown>;
  get(key: string): Promise<{ text(): Promise<string> } | null>;
  list(opts: { prefix: string }): Promise<{ objects: { key: string }[] }>;
}

/**
 * R2LedgerStore writes one immutable object per record at
 * `ledger/<runId>/<zero-padded-seq>.json`. Keys sort lexically in seq order, and
 * one-object-per-record suits an object-locked (WORM) bucket: each record is
 * written once and never mutated, so the store is append-only at the storage
 * layer too. (Enabling R2 Object Lock on the bucket makes that physical.)
 */
export class R2LedgerStore implements LedgerStore {
  constructor(private readonly r2: R2Like) {}
  private key(runId: string, seq: number): string {
    return `ledger/${runId}/${String(seq).padStart(6, "0")}.json`;
  }
  async list(runId: string): Promise<Record[]> {
    const res = await this.r2.list({ prefix: `ledger/${runId}/` });
    const keys = res.objects.map((o) => o.key).sort();
    const out: Record[] = [];
    for (const k of keys) {
      const obj = await this.r2.get(k);
      if (obj) out.push(JSON.parse(await obj.text()) as Record);
    }
    return out;
  }
  async append(runId: string, record: Record): Promise<void> {
    await this.r2.put(this.key(runId, record.seq), JSON.stringify(record));
  }
}

/**
 * LedgerWriter appends signed, chained records for one run to a store. Appends on
 * a single run are serialized by the caller (the Workflow drives steps in order),
 * so no locking is needed here.
 */
export class LedgerWriter {
  constructor(private readonly store: LedgerStore, private readonly signer: Signer) {}

  async append(runId: string, kind: string, payload: Record_Payload): Promise<Record> {
    const existing = await this.store.list(runId);
    const seq = existing.length;
    const prevHash = seq > 0 ? existing[seq - 1].hash : GENESIS_PREV;
    const at = new Date().toISOString();
    // Copy payload so a later caller mutation can't change what we hashed.
    const p: Record_Payload = {};
    for (const [k, v] of Object.entries(payload)) p[k] = v;
    const hash = await canonicalHash(seq, prevHash, runId, kind, at, p);
    const signature = await this.signer.sign(hash);
    const record: Record = { seq, prevHash, runId, kind, at, payload: p, hash, signature };
    await this.store.append(runId, record);
    return record;
  }
}

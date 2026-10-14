/**
 * Per-run HMAC for the event ingress (GO_PRODUCTION_PLAN.md §3 Phase 3b, C10).
 * Each run gets a secret minted at creation; the compute/Workflow side signs every
 * `POST /runs/:id/event` body with it, and the Orchestrator verifies the signature
 * before accepting the event. This stops an unauthenticated or cross-run caller
 * from injecting events into a run's stream. Uses WebCrypto (`crypto.subtle`),
 * which is a global in both the Workers runtime and Node >= 20.
 */

const enc = new TextEncoder();

/** The header the signature travels in. */
export const SIGNATURE_HEADER = "x-braid-signature";

async function hmacKey(secret: string): Promise<CryptoKey> {
  return crypto.subtle.importKey(
    "raw",
    enc.encode(secret),
    { name: "HMAC", hash: "SHA-256" },
    false,
    ["sign", "verify"],
  );
}

function toHex(buf: ArrayBuffer): string {
  const bytes = new Uint8Array(buf);
  let out = "";
  for (const b of bytes) out += b.toString(16).padStart(2, "0");
  return out;
}

/** sign returns the lowercase hex HMAC-SHA256 of `body` under `secret`. */
export async function sign(secret: string, body: string): Promise<string> {
  const key = await hmacKey(secret);
  const mac = await crypto.subtle.sign("HMAC", key, enc.encode(body));
  return toHex(mac);
}

/**
 * timingSafeEqual compares two hex strings in constant time w.r.t. their
 * contents, so a verifier does not leak how many leading characters matched. A
 * length mismatch short-circuits (the lengths are not secret).
 */
export function timingSafeEqual(a: string, b: string): boolean {
  if (a.length !== b.length) return false;
  let diff = 0;
  for (let i = 0; i < a.length; i++) diff |= a.charCodeAt(i) ^ b.charCodeAt(i);
  return diff === 0;
}

/**
 * verify recomputes the HMAC of `body` and compares it, in constant time, to the
 * provided signature. A missing/empty signature is false, never an exception.
 */
export async function verify(secret: string, body: string, signature: string | null): Promise<boolean> {
  if (!signature) return false;
  const expected = await sign(secret, body);
  return timingSafeEqual(expected, signature.trim().toLowerCase());
}

/** newSecret mints a random 256-bit secret as hex (one per run). */
export function newSecret(): string {
  const bytes = new Uint8Array(32);
  crypto.getRandomValues(bytes);
  let out = "";
  for (const b of bytes) out += b.toString(16).padStart(2, "0");
  return out;
}

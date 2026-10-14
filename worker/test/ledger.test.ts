import { describe, it, expect } from "vitest";
import {
  canonicalHash,
  contentHash,
  Ed25519Signer,
  LedgerWriter,
  type LedgerStore,
  type Record,
} from "../src/v1/ledger";

// Interop vector emitted by internal/ledger/vector_test.go. If the Go canonical
// hash construction changes, this breaks — which is the point: the TS writer and
// the Go verifier must agree byte-for-byte.
const VEC = {
  hash: "0fb63095bd9154793217ff74cb7027afadcee2ecc0e8dac3a96acabe2d022954",
  pubHex: "79b5562e8fe654f94078b112e8a98ba7901f853ae695bed7e0e3910bad049664",
  sigB64: "UoEwM9LB8zVm9Px8kxj9Tsu9yg0lAwt6Vr/jf9+hYYUxrpEUObqtN2DHR/Ttn9vktLG407XZ8bRwqdiEnut4Cw==",
  seedHex: "0102030405060708090a0b0c0d0e0f101112131415161718191a1b1c1d1e1f20",
};

describe("ledger canonical hash (Go interop)", () => {
  it("matches the Go CanonicalHash vector byte-for-byte", async () => {
    const h = await canonicalHash(0, "GENESIS", "run-1", "verify", "2026-01-02T03:04:05Z", {
      passed: "true",
      tests: "12",
      rung: "4",
    });
    expect(h).toBe(VEC.hash);
  });

  it("is independent of payload key insertion order (canonical ordering)", async () => {
    const a = await canonicalHash(1, "p", "r", "k", "t", { b: "2", a: "1", c: "3" });
    const b = await canonicalHash(1, "p", "r", "k", "t", { c: "3", a: "1", b: "2" });
    expect(a).toBe(b);
  });

  it("length-prefixes parts unambiguously (ab,c != a,bc)", async () => {
    expect(await contentHash(["ab", "c"])).not.toBe(await contentHash(["a", "bc"]));
  });
});

describe("ledger Ed25519 signing (Go interop)", () => {
  it("produces the deterministic signature Go produces for the same seed+hash", async () => {
    const signer = new Ed25519Signer(VEC.seedHex);
    const sig = await signer.sign(VEC.hash);
    expect(sig).toBe(VEC.sigB64);
  });
});

class MemLedgerStore implements LedgerStore {
  chains = new Map<string, Record[]>();
  async list(runId: string): Promise<Record[]> {
    return [...(this.chains.get(runId) ?? [])];
  }
  async append(runId: string, record: Record): Promise<void> {
    const c = this.chains.get(runId) ?? [];
    c.push(record);
    this.chains.set(runId, c);
  }
}

describe("LedgerWriter", () => {
  it("builds a linked, signed chain", async () => {
    const store = new MemLedgerStore();
    const w = new LedgerWriter(store, new Ed25519Signer(VEC.seedHex));
    await w.append("run-z", "snapshot", { baselineSha: "aaa" });
    await w.append("run-z", "verify", { passed: "true" });
    await w.append("run-z", "publish", { mergedSha: "bbb" });

    const chain = await store.list("run-z");
    expect(chain.map((r) => r.seq)).toEqual([0, 1, 2]);
    expect(chain[0].prevHash).toBe("GENESIS");
    expect(chain[1].prevHash).toBe(chain[0].hash);
    expect(chain[2].prevHash).toBe(chain[1].hash);
    for (const r of chain) {
      expect(r.hash).toHaveLength(64);
      expect(r.signature.length).toBeGreaterThan(0);
    }
  });
});

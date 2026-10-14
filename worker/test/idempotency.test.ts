import { describe, it, expect } from "vitest";
import { MapIdempotencyStore, begin, fingerprint } from "../src/v1/idempotency";

describe("idempotency", () => {
  it("proceeds without dedup when no key is supplied", async () => {
    const store = new MapIdempotencyStore();
    const out = await begin(store, null, "{}");
    expect(out.kind).toBe("proceed");
  });

  it("stores on first use and replays the stored result on retry", async () => {
    const store = new MapIdempotencyStore();
    const body = JSON.stringify({ base: "b", agents: ["a1"] });

    const first = await begin(store, "key-1", body);
    expect(first.kind).toBe("proceed");
    if (first.kind !== "proceed") return;
    await first.commit(201, JSON.stringify({ id: "run-xyz" }));

    const retry = await begin(store, "key-1", body);
    expect(retry.kind).toBe("replay");
    if (retry.kind !== "replay") return;
    expect(retry.status).toBe(201);
    expect(JSON.parse(retry.body)).toEqual({ id: "run-xyz" });
  });

  it("409s when the same key is reused with a different body", async () => {
    const store = new MapIdempotencyStore();
    const first = await begin(store, "key-2", JSON.stringify({ base: "b1" }));
    if (first.kind !== "proceed") throw new Error("expected proceed");
    await first.commit(201, "{}");

    const reused = await begin(store, "key-2", JSON.stringify({ base: "b2-different" }));
    expect(reused.kind).toBe("conflict");
  });

  it("fingerprint is stable and body-sensitive", async () => {
    expect(await fingerprint("a")).toBe(await fingerprint("a"));
    expect(await fingerprint("a")).not.toBe(await fingerprint("b"));
    expect(await fingerprint("a")).toMatch(/^[0-9a-f]{64}$/);
  });
});

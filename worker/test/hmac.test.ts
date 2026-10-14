import { describe, it, expect } from "vitest";
import { sign, verify, timingSafeEqual, newSecret, SIGNATURE_HEADER } from "../src/v1/hmac";

describe("hmac", () => {
  it("round-trips sign/verify", async () => {
    const secret = "run-secret-abc";
    const body = JSON.stringify({ type: "verify", runId: "r1", seq: 3 });
    const sig = await sign(secret, body);
    expect(await verify(secret, body, sig)).toBe(true);
  });

  it("rejects a wrong secret, a tampered body, and a missing signature", async () => {
    const body = "payload";
    const sig = await sign("right", body);
    expect(await verify("wrong", body, sig)).toBe(false);
    expect(await verify("right", "payload-tampered", sig)).toBe(false);
    expect(await verify("right", body, null)).toBe(false);
    expect(await verify("right", body, "")).toBe(false);
  });

  it("accepts an upper-case / padded signature header value", async () => {
    const body = "x";
    const sig = await sign("s", body);
    expect(await verify("s", body, `  ${sig.toUpperCase()}  `)).toBe(true);
  });

  it("timingSafeEqual is correct and length-aware", () => {
    expect(timingSafeEqual("abcd", "abcd")).toBe(true);
    expect(timingSafeEqual("abcd", "abce")).toBe(false);
    expect(timingSafeEqual("abc", "abcd")).toBe(false);
  });

  it("mints distinct 256-bit hex secrets", () => {
    const a = newSecret();
    const b = newSecret();
    expect(a).toMatch(/^[0-9a-f]{64}$/);
    expect(a).not.toBe(b);
  });

  it("exposes the header name", () => {
    expect(SIGNATURE_HEADER).toBe("x-braid-signature");
  });
});

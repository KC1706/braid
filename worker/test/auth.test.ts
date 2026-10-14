import { describe, it, expect } from "vitest";
import {
  MapTokenStore,
  authenticate,
  authorize,
  can,
  bearer,
  type Principal,
} from "../src/v1/auth";

const admin: Principal = { id: "u-admin", roles: ["admin"] };
const operator: Principal = { id: "u-op", roles: ["operator"] };
const viewer: Principal = { id: "u-view", roles: ["viewer"] };

function store() {
  return new MapTokenStore(
    new Map([
      ["tok-admin", admin],
      ["tok-op", operator],
      ["tok-view", viewer],
    ]),
  );
}

const reqWith = (token?: string) =>
  new Request("https://x/v1/runs", {
    method: "POST",
    headers: token ? { authorization: `Bearer ${token}` } : {},
  });

describe("bearer", () => {
  it("parses the Authorization header, case-insensitively", () => {
    expect(bearer(new Request("https://x", { headers: { authorization: "Bearer abc" } }))).toBe("abc");
    expect(bearer(new Request("https://x", { headers: { authorization: "bearer  xyz " } }))).toBe("xyz");
    expect(bearer(new Request("https://x"))).toBeNull();
  });
});

describe("can", () => {
  it("encodes the role policy", () => {
    expect(can(viewer, "runs:read")).toBe(true);
    expect(can(viewer, "runs:create")).toBe(false);
    expect(can(viewer, "runs:cancel")).toBe(false);
    expect(can(operator, "runs:create")).toBe(true);
    expect(can(operator, "runs:approve")).toBe(true);
    expect(can(admin, "runs:approve")).toBe(true);
  });
});

describe("authenticate", () => {
  it("401s a missing or unknown token, resolves a known one", async () => {
    expect(await authenticate(reqWith(), store())).toMatchObject({ ok: false, status: 401 });
    expect(await authenticate(reqWith("nope"), store())).toMatchObject({ ok: false, status: 401 });
    const ok = await authenticate(reqWith("tok-op"), store());
    expect(ok).toMatchObject({ ok: true, principal: { id: "u-op" } });
  });
});

describe("authorize", () => {
  it("403s an authenticated-but-unpermitted principal", async () => {
    const res = await authorize(reqWith("tok-view"), store(), "runs:create");
    expect(res).toMatchObject({ ok: false, status: 403 });
  });
  it("allows a permitted principal", async () => {
    const res = await authorize(reqWith("tok-op"), store(), "runs:create");
    expect(res.ok).toBe(true);
  });
  it("401 (not 403) when the token itself is bad", async () => {
    const res = await authorize(reqWith("nope"), store(), "runs:read");
    expect(res).toMatchObject({ ok: false, status: 401 });
  });
});

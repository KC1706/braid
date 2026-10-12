import { describe, it, expect } from "vitest";
import app from "../src/index.js";

// These baseline tests must keep passing after the swarm's changes are merged.
// They are the deterministic acceptance gate the integrator re-runs.
describe("GET /users/:id", () => {
  it("returns a known user", async () => {
    const res = await app.request("/users/1");
    expect(res.status).toBe(200);
    const body = await res.json();
    expect(body).toMatchObject({ id: "1", name: "Ada Lovelace" });
  });

  it("404s an unknown user", async () => {
    const res = await app.request("/users/999");
    expect(res.status).toBe(404);
  });
});

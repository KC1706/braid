/**
 * Runtime adapter from Braid's zone-client interfaces (ztypes.ts) to a
 * @cloudflare/containers Container (GO_PRODUCTION_PLAN.md §3 Phase 3c). This is
 * the only file that imports the container runtime, so the pure orchestration
 * and its tests never pull `cloudflare:workers` into node. It is exercised once
 * deployed, not in the node unit suite.
 */

import { getContainer } from "@cloudflare/containers";
import {
  BaseMovedError,
  type ComputeZoneClient,
  type IOZoneClient,
  type IntegrateRequest,
  type IntegrateResponse,
  type PublishRequest,
  type PublishResponse,
  type SnapshotRequest,
  type SnapshotResponse,
} from "./ztypes";

// Re-export the wire types so existing importers of ./zones keep working.
export * from "./ztypes";

/** The subset of a @cloudflare/containers Container stub the client uses. */
export interface ContainerStub {
  startAndWaitForPorts(opts?: {
    ports?: number | number[];
    startOptions?: { envVars?: Record<string, string>; enableInternet?: boolean };
  }): Promise<void>;
  containerFetch(url: string, init?: RequestInit, port?: number): Promise<Response>;
}

/** A DO namespace for a container class (what getContainer takes). */
export type ContainerNamespace = Parameters<typeof getContainer>[0];

const PORT = 8080;

async function postZone(stub: ContainerStub, path: string, body: unknown): Promise<Response> {
  return stub.containerFetch(
    `http://container${path}`,
    { method: "POST", headers: { "content-type": "application/json" }, body: JSON.stringify(body) },
    PORT,
  );
}

/**
 * ContainerZoneClient drives one zone's Container. `internet` is false for the
 * compute zone (default-deny egress, enforced per start) and true (host-scoped at
 * the platform level) for the io zone. The per-instance name keys the Container DO
 * so a run always reaches a consistent instance.
 */
export class ContainerZoneClient implements IOZoneClient, ComputeZoneClient {
  constructor(
    private readonly ns: ContainerNamespace,
    private readonly name: string,
    private readonly internet: boolean,
  ) {}

  private async stub(): Promise<ContainerStub> {
    const c = getContainer(this.ns, this.name) as unknown as ContainerStub;
    await c.startAndWaitForPorts({ ports: PORT, startOptions: { enableInternet: this.internet } });
    return c;
  }

  async snapshot(req: SnapshotRequest): Promise<SnapshotResponse> {
    const res = await postZone(await this.stub(), "/io/snapshot", req);
    if (!res.ok) throw new Error(`snapshot: ${res.status} ${await res.text()}`);
    return (await res.json()) as SnapshotResponse;
  }

  async integrate(req: IntegrateRequest): Promise<IntegrateResponse> {
    const res = await postZone(await this.stub(), "/compute/integrate", req);
    if (!res.ok) throw new Error(`integrate: ${res.status} ${await res.text()}`);
    return (await res.json()) as IntegrateResponse;
  }

  async publish(req: PublishRequest): Promise<PublishResponse> {
    const res = await postZone(await this.stub(), "/io/publish", req);
    if (res.status === 409) throw new BaseMovedError();
    if (!res.ok) throw new Error(`publish: ${res.status} ${await res.text()}`);
    return (await res.json()) as PublishResponse;
  }
}

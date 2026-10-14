/**
 * The two Container classes for Phase 3c. Both run the same braid image; the
 * zone each serves is fixed by BRAID_ZONE, and the security posture is fixed by
 * enableInternet:
 *
 *   - ComputeZone runs the merge engine + the agents' tests. enableInternet is
 *     false (default-deny egress) and it is NEVER handed an Artifacts token, so
 *     untrusted agent code can neither phone home nor read a credential.
 *   - IOZone runs only git (clone/fetch/CAS-push) with the scoped tokens the
 *     Worker passes per request. It needs egress to the Artifacts host, so
 *     internet is enabled; it never executes untrusted code.
 *
 * sleepAfter keeps an instance warm briefly so the snapshot -> integrate ->
 * publish calls of one run reuse it instead of cold-starting each time.
 */

import { Container } from "@cloudflare/containers";

export class ComputeZone extends Container {
  defaultPort = 8080;
  sleepAfter = "3m";
  enableInternet = false; // default-deny egress (C13/C15)
  envVars = { BRAID_ZONE: "compute" };
}

export class IOZone extends Container {
  defaultPort = 8080;
  sleepAfter = "3m";
  enableInternet = true; // git I/O to the Artifacts host
  envVars = { BRAID_ZONE: "io" };
}

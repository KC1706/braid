/**
 * Base-Coordinator state machine (GO_PRODUCTION_PLAN.md §3 Phase 4a, C8). One
 * instance per base (a Durable Object in prod) holds the base's current tip SHA
 * and a single publish lease, so two runs targeting the same base cannot clobber
 * each other:
 *
 *   - A run calls beginPublish(runId, expectedBaseSha): it is granted the lease
 *     only if the lease is free (or already its) AND the base tip it merged from
 *     still matches the coordinator's current tip (compare-and-swap on the parent
 *     SHA). A stale base -> "base_moved" (rebase + retry); a lease held by another
 *     run -> "busy" (wait + retry).
 *   - On a successful push the run calls commitPublish(runId, newTip): the tip
 *     advances and the lease is released. abortPublish releases without advancing.
 *
 * This is the application-level serialization in front of gitio's git-level
 * `--force-with-lease`; the git CAS remains the ultimate guard, so even a
 * coordinator bug cannot cause a non-fast-forward overwrite. The lease carries a
 * TTL so a crashed run cannot deadlock a base forever.
 *
 * The module is pure (no I/O, no clock): the caller passes `now` and persists the
 * returned state. coordinator-do.ts is the DO adapter.
 */

export interface Lease {
  holder: string; // runId
  expiresAt: string; // RFC3339
}

export interface CoordinatorState {
  /** The base's current published tip SHA, or null before the first run (TOFU). */
  currentTip: string | null;
  lease: Lease | null;
}

export function initialState(): CoordinatorState {
  return { currentTip: null, lease: null };
}

export type BeginResult =
  | { status: "granted"; currentTip: string; expiresAt: string }
  | { status: "busy"; holder: string }
  | { status: "base_moved"; currentTip: string };

export type CommitResult = { status: "ok"; currentTip: string } | { status: "not_holder" };

/** activeLease returns the lease if present and not expired as of `now`. */
function activeLease(state: CoordinatorState, now: string): Lease | null {
  if (!state.lease) return null;
  return state.lease.expiresAt > now ? state.lease : null; // RFC3339 sorts lexically
}

/**
 * beginPublish grants `runId` the exclusive publish lease iff no other run holds
 * a live lease AND expectedBaseSha matches the current tip (CAS on the parent
 * SHA). The first run on a fresh base adopts its expectedBaseSha as the tip
 * (trust-on-first-use); thereafter the CAS is enforced. Re-granting to the
 * current holder is idempotent, so a Workflow step retry is safe.
 */
export function beginPublish(
  state: CoordinatorState,
  now: string,
  runId: string,
  expectedBaseSha: string,
  ttlSeconds: number,
): { state: CoordinatorState; result: BeginResult } {
  const live = activeLease(state, now);
  if (live && live.holder !== runId) {
    return { state, result: { status: "busy", holder: live.holder } };
  }
  const tip = state.currentTip ?? expectedBaseSha; // TOFU on a fresh base
  if (expectedBaseSha !== tip) {
    return { state, result: { status: "base_moved", currentTip: tip } };
  }
  const expiresAt = new Date(Date.parse(now) + ttlSeconds * 1000).toISOString();
  const next: CoordinatorState = { currentTip: tip, lease: { holder: runId, expiresAt } };
  return { state: next, result: { status: "granted", currentTip: tip, expiresAt } };
}

/**
 * commitPublish advances the tip to newTip and releases the lease — but only if
 * `runId` still holds a live lease. A non-holder (expired lease, or a different
 * run) is rejected without changing the tip, so a late commit from a timed-out
 * run cannot overwrite a newer one.
 */
export function commitPublish(
  state: CoordinatorState,
  now: string,
  runId: string,
  newTip: string,
): { state: CoordinatorState; result: CommitResult } {
  const live = activeLease(state, now);
  if (!live || live.holder !== runId) {
    return { state, result: { status: "not_holder" } };
  }
  const next: CoordinatorState = { currentTip: newTip, lease: null };
  return { state: next, result: { status: "ok", currentTip: newTip } };
}

/** abortPublish releases the lease if held by `runId`, without advancing the tip. */
export function abortPublish(state: CoordinatorState, now: string, runId: string): CoordinatorState {
  const live = activeLease(state, now);
  if (live && live.holder === runId) {
    return { ...state, lease: null };
  }
  return state;
}

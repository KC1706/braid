/**
 * AuthN/Z for the /v1 control plane (GO_PRODUCTION_PLAN.md §3 Phase 3b). A caller
 * presents a bearer capability token; a TokenStore resolves it to a Principal
 * carrying roles. Authorization is role → allowed-action, so there is zero
 * unauthenticated surface on /v1 and a viewer token cannot start or cancel a run.
 *
 * The store is an interface: a secret-map in dev, KV / a DO / an external issuer
 * in prod. Token lookup is the only async bit; the role→action policy is pure.
 */

export type Role = "admin" | "operator" | "viewer";

/** Actions a request can require. */
export type Action = "runs:create" | "runs:read" | "runs:cancel" | "runs:approve";

export interface Principal {
  id: string;
  roles: Role[];
}

/** role → the actions it grants. admin is the superset. */
const grants: Record<Role, ReadonlySet<Action>> = {
  admin: new Set<Action>(["runs:create", "runs:read", "runs:cancel", "runs:approve"]),
  operator: new Set<Action>(["runs:create", "runs:read", "runs:cancel", "runs:approve"]),
  viewer: new Set<Action>(["runs:read"]),
};

/** can reports whether any of the principal's roles grants `action`. */
export function can(principal: Principal, action: Action): boolean {
  return principal.roles.some((r) => grants[r]?.has(action));
}

/** Resolves a bearer token to a principal, or null if the token is unknown. */
export interface TokenStore {
  resolve(token: string): Promise<Principal | null>;
}

/** A simple in-memory token store (dev / tests). */
export class MapTokenStore implements TokenStore {
  constructor(private readonly tokens: Map<string, Principal>) {}
  async resolve(token: string): Promise<Principal | null> {
    return this.tokens.get(token) ?? null;
  }
}

/** Extracts a bearer token from the Authorization header, or null. */
export function bearer(req: Request): string | null {
  const h = req.headers.get("authorization") ?? "";
  const m = /^Bearer\s+(.+)$/i.exec(h.trim());
  return m ? m[1].trim() : null;
}

export type AuthResult =
  | { ok: true; principal: Principal }
  | { ok: false; status: 401 | 403; error: string };

/**
 * authenticate resolves the request's bearer token to a principal. A missing or
 * unknown token is a 401. It does NOT check authorization — pair it with `can`
 * (or use `authorize`) at the handler.
 */
export async function authenticate(req: Request, store: TokenStore): Promise<AuthResult> {
  const token = bearer(req);
  if (!token) return { ok: false, status: 401, error: "missing bearer token" };
  const principal = await store.resolve(token);
  if (!principal) return { ok: false, status: 401, error: "invalid token" };
  return { ok: true, principal };
}

/**
 * authorize authenticates and then checks that the principal may perform
 * `action`, returning a 403 if not. This is the single call a /v1 handler makes.
 */
export async function authorize(req: Request, store: TokenStore, action: Action): Promise<AuthResult> {
  const auth = await authenticate(req, store);
  if (!auth.ok) return auth;
  if (!can(auth.principal, action)) {
    return { ok: false, status: 403, error: `principal ${auth.principal.id} lacks ${action}` };
  }
  return auth;
}

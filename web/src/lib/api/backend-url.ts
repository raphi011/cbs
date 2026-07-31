// Where each operator's listener is bound. Server-side only: this is deployment
// topology, which is not domain data and appears in no DTO — a GET /members that
// returned base URLs would make the member roster a deployment manifest.
//
// It is a module rather than inline in the proxy because two things need it now:
// the proxy, which forwards to one of these, and /api/operators, which probes all
// of them so the lobby can tell an un-provisioned bank from a running one.

export interface BackendConfig {
  host: string;
  basePort: number;
  overrides: Record<string, string>;
}

export type Institution = "central-bank" | "clearing-house";

// The answer /api/operators gives. It lives here rather than in endpoints.ts so
// the route handler that produces it and the client function that consumes it
// share one definition instead of agreeing by hand.
export interface OperatorStatus {
  operator: string;
  live: boolean;
}

// Reads the environment into a value, so the derivation below is a pure function
// a test can hold. BACKENDS is a JSON object of operator key to base URL; a
// bank's key is its participant id.
export function backendConfig(env: Record<string, string | undefined>): BackendConfig {
  return {
    host: env.BACKEND_HOST ?? "http://localhost",
    basePort: Number(env.BASE_PORT ?? 8081),
    overrides: env.BACKENDS ? (JSON.parse(env.BACKENDS) as Record<string, string>) : {},
  };
}

// The two institutions sit at the base port and the next one, mirroring
// cmd/server's plan(). With no configuration at all, `make dev` works and the
// ports are predictable.
export function institutionUrl(key: Institution, cfg: BackendConfig): string {
  if (cfg.overrides[key]) return cfg.overrides[key];
  return `${cfg.host}:${cfg.basePort + (key === "central-bank" ? 0 : 1)}`;
}

// A bank's port depends on where it sits in the roster, which only the clearing
// house can answer — and on nothing about its id. The seed's ids are bank_1,
// bank_3, bank_5, bank_7: deliberately not contiguous, so an id-derived port
// would be wrong on the very first dataset.
export function bankUrl(pid: string, roster: string[], cfg: BackendConfig): string {
  if (cfg.overrides[pid]) return cfg.overrides[pid];
  const index = roster.indexOf(pid);
  if (index < 0) {
    throw new Error(
      `no listener for bank ${pid}. A participant admitted at runtime has no ` +
        `listener until the server restarts — admission is not provisioning.`,
    );
  }
  return `${cfg.host}:${cfg.basePort + 2 + index}`;
}

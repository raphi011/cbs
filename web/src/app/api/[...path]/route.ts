import { type NextRequest, NextResponse } from "next/server";

import {
  backendConfig,
  bankUrl,
  institutionUrl,
  type Institution,
} from "@/lib/api/backend-url";

// This catch-all Route Handler proxies every browser request to the Go backend.
// Because the browser only ever talks to its own origin (/api/...), CORS is
// impossible by construction. We normalize transport errors into a clean 502 so
// the client's {error} parsing is uniform.
//
// Since the operator split there is no single backend: each entity has a
// listener of its own. The first segment after /api names the operator —
// `central-bank`, `clearing-house`, or `bank/<pid>` — and is stripped before
// the rest is forwarded. See src/lib/api/operator.ts, which builds those paths.
//
// Route Handlers are not cached by default in Next 16, but we force-dynamic to
// be explicit: this is a live proxy, never prerendered.
export const dynamic = "force-dynamic";

// Read once per process: the environment does not change under a running server,
// and JSON.parse of BACKENDS on every request would be waste.
const CFG = backendConfig(process.env);
const CENTRAL_BANK = "central-bank";
const CLEARING_HOUSE = "clearing-house";

// A bank's port depends on where it sits in the roster, which only the clearing
// house can answer. The roster is read once and cached for the life of the
// process: ports are static by design — a bank admitted at runtime gets no
// listener until a restart — so a stale answer is not a risk, and re-reading it
// per request would put a network hop in front of every call.
let rosterCache: Promise<string[]> | null = null;

function roster(): Promise<string[]> {
  if (!rosterCache) {
    rosterCache = fetch(`${institutionUrl(CLEARING_HOUSE, CFG)}/members`, {
      cache: "no-store",
    })
      .then((r) => (r.ok ? r.json() : Promise.reject(new Error("roster unavailable"))))
      .then((members: { id: string }[]) => members.map((m) => m.id))
      .catch((err) => {
        rosterCache = null; // a failed read must not be cached
        throw err;
      });
  }
  return rosterCache;
}

// resolve splits an incoming path into the backend that serves it and the path
// that backend expects. Returns null when the first segment names no operator.
async function resolve(
  segments: string[],
): Promise<{ base: string; rest: string[]; key: string } | null> {
  const [head, ...tail] = segments;
  if (head === CENTRAL_BANK || head === CLEARING_HOUSE) {
    return { base: institutionUrl(head as Institution, CFG), rest: tail, key: head };
  }
  if (head === "bank") {
    const [pid, ...rest] = tail;
    if (!pid) return null;
    return { base: bankUrl(pid, await roster(), CFG), rest, key: `bank/${pid}` };
  }
  return null;
}

// In Next 16, dynamic route params are async — `ctx.params` is a Promise and
// must be awaited. Typing it inline avoids depending on generated RouteContext
// types during `tsc --noEmit`.
async function handle(
  request: NextRequest,
  ctx: { params: Promise<{ path: string[] }> },
) {
  const { path } = await ctx.params;

  let target: { base: string; rest: string[]; key: string } | null;
  try {
    target = await resolve(path ?? []);
  } catch (err) {
    return NextResponse.json(
      { error: err instanceof Error ? err.message : String(err) },
      { status: 502 },
    );
  }
  if (!target) {
    return NextResponse.json(
      {
        error:
          `"${(path ?? []).join("/")}" names no operator. A request must say ` +
          `which listener it is for: central-bank, clearing-house, or bank/<pid>.`,
      },
      { status: 400 },
    );
  }

  const url = `${target.base}/${target.rest.join("/")}${request.nextUrl.search}`;

  // Forward only the content type; let undici set host/length/encoding. This
  // strips hop-by-hop headers that would otherwise confuse the upstream.
  const headers = new Headers();
  const contentType = request.headers.get("content-type");
  if (contentType) headers.set("content-type", contentType);

  let body: string | undefined;
  if (request.method !== "GET" && request.method !== "HEAD") {
    body = await request.text();
  }

  let upstream: Response;
  try {
    upstream = await fetch(url, {
      method: request.method,
      headers,
      body,
      cache: "no-store",
    });
  } catch {
    // Naming the operator matters now that there are six of them: "the backend
    // is down" is a different problem from "this one bank is down".
    return NextResponse.json(
      { error: `Backend for ${target.key} unreachable at ${target.base}` },
      { status: 502 },
    );
  }

  // Return the upstream status and body verbatim so the client sees the
  // backend's descriptive {error} string on failures.
  const text = await upstream.text();
  return new NextResponse(text || null, {
    status: upstream.status,
    headers: {
      "content-type":
        upstream.headers.get("content-type") ?? "application/json",
    },
  });
}

export const GET = handle;
export const POST = handle;
export const DELETE = handle;
export const OPTIONS = handle;

import { type NextRequest, NextResponse } from "next/server";

// This catch-all Route Handler proxies every browser request to the Go backend.
// Because the browser only ever talks to its own origin (/api/...), CORS is
// impossible by construction. We normalize transport errors into a clean 502 so
// the client's {error} parsing is uniform.
//
// Route Handlers are not cached by default in Next 16, but we force-dynamic to
// be explicit: this is a live proxy, never prerendered.
export const dynamic = "force-dynamic";

const BACKEND = process.env.BACKEND_URL ?? "http://localhost:8080";

// In Next 16, dynamic route params are async — `ctx.params` is a Promise and
// must be awaited. Typing it inline avoids depending on generated RouteContext
// types during `tsc --noEmit`.
async function handle(
  request: NextRequest,
  ctx: { params: Promise<{ path: string[] }> },
) {
  const { path } = await ctx.params;
  const target = `${BACKEND}/${(path ?? []).join("/")}${request.nextUrl.search}`;

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
    upstream = await fetch(target, {
      method: request.method,
      headers,
      body,
      cache: "no-store",
    });
  } catch {
    return NextResponse.json(
      { error: `Backend unreachable at ${BACKEND}` },
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

// Single fetch wrapper for the whole app. Every call goes to the same-origin
// proxy at /api/<backend-path>, so the Go origin is never exposed to the
// browser and CORS can't happen. On non-2xx it throws ApiError{status,message},
// preferring the backend's {error} string.

import { ApiError } from "./errors";

const BASE = "/api";

export async function request<T>(
  method: string,
  path: string,
  body?: unknown,
): Promise<T> {
  const hasBody = body !== undefined;
  const res = await fetch(BASE + path, {
    method,
    headers: hasBody ? { "Content-Type": "application/json" } : undefined,
    body: hasBody ? JSON.stringify(body) : undefined,
  });

  const text = await res.text();
  let data: unknown = undefined;
  if (text) {
    try {
      data = JSON.parse(text);
    } catch {
      data = text;
    }
  }

  if (!res.ok) {
    const serverMessage =
      data && typeof data === "object" && "error" in data
        ? String((data as { error: unknown }).error)
        : text || res.statusText;
    throw new ApiError(res.status, serverMessage);
  }

  // 204 / empty body: return undefined as T.
  return data as T;
}

// qs builds a query string from a params object, dropping undefined/null/empty
// values. Returns "" or "?a=1&b=2".
export function qs(
  params: Record<string, string | number | undefined | null>,
): string {
  const sp = new URLSearchParams();
  for (const [key, value] of Object.entries(params)) {
    if (value !== undefined && value !== null && value !== "") {
      sp.set(key, String(value));
    }
  }
  const s = sp.toString();
  return s ? `?${s}` : "";
}

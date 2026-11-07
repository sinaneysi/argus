// The transport to the Go JSON API (apps/api, /api/v1). Server side only: API_URL is never sent to the browser.
// The visitor's cookies, address and user agent are forwarded so the API sees the same session, lockout address
// and device as on its own pages.

export type ApiResponse<T> = { status: number; data: T; setCookies: string[] };

export function apiBase(): string {
  return (process.env.API_URL ?? "http://127.0.0.1:8080").replace(/\/+$/, "");
}

export function forwardHeaders(incoming: Headers): Headers {
  const h = new Headers({ accept: "application/json" });
  const ck = incoming.get("cookie");
  if (ck) h.set("cookie", ck);
  const ip = incoming.get("x-real-ip") ?? incoming.get("x-forwarded-for")?.split(",")[0]?.trim();
  if (ip && /^[0-9a-fA-F.:]{2,45}$/.test(ip)) h.set("x-real-ip", ip);
  const ua = incoming.get("user-agent");
  if (ua) h.set("user-agent", ua.slice(0, 400));
  return h;
}

export type CallOptions = {
  body?: Record<string, string | string[] | number | boolean | undefined>;
  csrf?: string;
  fetchImpl?: typeof fetch;
};

export async function apiCall<T>(method: "GET" | "POST", path: string, incoming: Headers, opts: CallOptions = {}): Promise<ApiResponse<T>> {
  if (!/^\/api\/v1\/[a-z0-9/_-]+(\?[^#]*)?$/i.test(path)) throw new Error(`not an API path: ${path}`);
  const h = forwardHeaders(incoming);
  let body: string | undefined;
  if (method === "POST") {
    h.set("content-type", "application/json");
    if (opts.csrf) h.set("x-csrf-token", opts.csrf);
    const clean: Record<string, string | string[] | number | boolean> = {};
    for (const [k, v] of Object.entries(opts.body ?? {})) if (v !== undefined) clean[k] = v;
    body = JSON.stringify(clean);
  }
  const res = await (opts.fetchImpl ?? fetch)(apiBase() + path, { method, headers: h, body, cache: "no-store", redirect: "manual", signal: AbortSignal.timeout(10000) });
  let data: unknown = null;
  try {
    data = await res.json();
  } catch {
    data = { error: `The API answered ${res.status} without JSON` };
  }
  const setCookies = typeof res.headers.getSetCookie === "function" ? res.headers.getSetCookie() : [];
  return { status: res.status, data: data as T, setCookies };
}

/** The error text of a refusal, or a generic one. */
export function errorOf(data: unknown, fallback = "That did not work."): string {
  if (data && typeof data === "object" && "error" in data && typeof (data as { error: unknown }).error === "string") {
    const e = (data as { error: string }).error;
    return e ? e.charAt(0).toUpperCase() + e.slice(1) : fallback;
  }
  return fallback;
}

/** A query string from a record, dropping empty values. */
export function qs(params: Record<string, string | number | undefined | null>): string {
  const u = new URLSearchParams();
  for (const [k, v] of Object.entries(params)) if (v !== undefined && v !== null && v !== "") u.set(k, String(v));
  const s = u.toString().replace(/%3A/g, ":");
  return s ? "?" + s : "";
}

// Server-only helpers: API reads with the visitor's cookies, writes from server actions that pass the API's cookies
// back to the browser, the CSRF token for forms, and the one-shot flash banner.
import { cache } from "react";
import { cookies, headers } from "next/headers";
import { revalidatePath } from "next/cache";
import { redirect } from "next/navigation";
import { apiCall, type ApiResponse } from "./api";
import { parseSetCookie } from "./cookies";
import { CSRF_COOKIE, FLASH_COOKIE } from "./names";
import type { Banner, Me, Nav, Status } from "./types";

/** The request's headers with the cookies as they stand now: an action that rotated the session re-renders the page in
 * the same response, and the request's own Cookie header still holds the token the API just ended. */
async function forwarded(): Promise<Headers> {
  const h = new Headers(await headers());
  h.set("cookie", (await cookies()).toString());
  return h;
}

export async function apiGet<T>(path: string): Promise<ApiResponse<T>> {
  return apiCall<T>("GET", path, await forwarded());
}

/** A write from a server action; the API's Set-Cookie headers go to the browser. */
export async function apiPost<T>(path: string, body: Record<string, string | string[] | number | boolean | undefined>, csrf: string): Promise<ApiResponse<T>> {
  const res = await apiCall<T>("POST", path, await forwarded(), { body, csrf });
  const jar = await cookies();
  for (const sc of res.setCookies) {
    const c = parseSetCookie(sc);
    if (!c) continue;
    jar.set({ name: c.name, value: c.value, path: c.path ?? "/", httpOnly: c.httpOnly, secure: c.secure, sameSite: c.sameSite, maxAge: c.maxAge });
  }
  return res;
}

export async function csrfToken(): Promise<string> {
  return (await cookies()).get(CSRF_COOKIE)?.value ?? "";
}

/** The form's token (double submit: the browser sends it in the body, the cookie rides along). */
export function formCsrf(fd: FormData): string {
  const v = fd.get("csrf");
  return typeof v === "string" ? v : "";
}

export function field(fd: FormData, k: string): string {
  const v = fd.get(k);
  return typeof v === "string" ? v.slice(0, 2000) : "";
}

export async function setFlash(text: string, kind: Banner["kind"] = "ok"): Promise<void> {
  (await cookies()).set({ name: FLASH_COOKIE, value: encodeURIComponent(JSON.stringify({ text: text.slice(0, 300), kind })),
    path: "/", maxAge: 60, httpOnly: true, secure: true, sameSite: "lax" });
}

/** Back to a page after a write. A submission from the hydrated page that returns to the page it was sent from is
 * re-rendered in the action's own response: no navigation, so the scroll position and focus stay where they were. A plain
 * POST (no JavaScript), another page or an anchor still redirect. Call it as `return back(url)`: it returns only when it
 * did not redirect. */
export async function back(to: string): Promise<void> {
  const h = await headers();
  const here = h.get("x-url");
  if (!h.has("next-action") || !here || to !== here) redirect(to);
  const jar = await cookies();
  const flash = jar.get(FLASH_COOKIE)?.value;
  // The flash shows in this response; Max-Age=0 so the browser drops it instead of showing it again on the next page.
  if (flash) jar.set({ name: FLASH_COOKIE, value: flash, path: "/", maxAge: 0, httpOnly: true, secure: true, sameSite: "lax" });
  revalidatePath(here.split("?")[0] || "/");
}

/** The flash message of this request: a page request has it in x-flash (middleware moved it there and cleared the
 * cookie); an action that stayed on its page (back) has it in the cookie it just set. */
export async function readFlash(): Promise<Banner | null> {
  const raw = (await cookies()).get(FLASH_COOKIE)?.value || (await headers()).get("x-flash");
  if (!raw) return null;
  try {
    const v = JSON.parse(decodeURIComponent(raw)) as Banner;
    if (typeof v.text === "string" && ["ok", "warn", "fail", "info"].includes(v.kind)) return { text: v.text, kind: v.kind };
  } catch {
    /* a broken flash is no flash */
  }
  return null;
}

export async function currentUrl(): Promise<string> {
  return (await headers()).get("x-url") ?? "/";
}

/** The signed-in user, or a redirect to sign-in (or to the account page a session must visit first). */
export async function requireMe(): Promise<Me> {
  const r = await apiGet<Me>("/api/v1/me");
  if (r.status === 401) redirect("/login?next=" + encodeURIComponent(await currentUrl()));
  if (r.status !== 200) throw new Error(`/api/v1/me answered ${r.status}`);
  const url = await currentUrl();
  const path = url.split("?")[0];
  if (r.data.mustChangePassword && path !== "/account") redirect("/account");
  if (r.data.mfa.enrolRequired && path !== "/account/2fa") redirect("/account/2fa?next=" + encodeURIComponent(url));
  return r.data;
}

/** What the app shell needs, read once per request: the layout and its sidebar and top bar slots (app/(app)/@side, @top)
 * share it on a document load, and the slots read it alone on a client navigation. */
export const shellMe = cache(requireMe);
export const shellNav = cache(() => apiGet<Nav>("/api/v1/nav"));
export const shellStatus = cache(() => apiGet<Status>("/api/v1/status"));

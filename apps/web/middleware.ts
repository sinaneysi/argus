import { NextResponse, type NextRequest } from "next/server";
import { CSRF_COOKIE, FLASH_COOKIE } from "@/lib/names";

// Every page request: a fresh CSP nonce (Next puts it on its own scripts), the double-submit CSRF cookie when it is
// missing, the one-shot flash message moved from its cookie into a request header, and the path for the layout's
// sign-in redirect.

const isDev = process.env.NODE_ENV !== "production";

export function cspFor(nonce: string): string {
  return [
    "default-src 'none'",
    `script-src 'self' 'nonce-${nonce}' 'strict-dynamic'${isDev ? " 'unsafe-eval'" : ""}`,
    `style-src 'self' 'nonce-${nonce}'`,
    "img-src 'self'",
    "font-src 'self'",
    `connect-src 'self'${isDev ? " ws:" : ""}`,
    "manifest-src 'self'",
    "base-uri 'none'",
    "form-action 'self'",
    "frame-ancestors 'none'",
  ].join("; ");
}

function token(): string {
  const b = new Uint8Array(24);
  crypto.getRandomValues(b);
  return Array.from(b, (x) => x.toString(16).padStart(2, "0")).join("");
}

export function middleware(req: NextRequest) {
  const nonce = btoa(crypto.randomUUID());
  const csp = cspFor(nonce);
  const headers = new Headers(req.headers);
  headers.set("x-nonce", nonce);
  headers.set("content-security-policy", csp);
  headers.set("x-url", req.nextUrl.pathname + req.nextUrl.search);
  headers.delete("x-flash"); // only ever set here

  let newCsrf = "";
  if (!/^[A-Za-z0-9_-]{16,128}$/.test(req.cookies.get(CSRF_COOKIE)?.value ?? "")) {
    newCsrf = token();
    const ck = req.headers.get("cookie");
    headers.set("cookie", (ck ? ck + "; " : "") + `${CSRF_COOKIE}=${newCsrf}`);
  }
  const flash = req.cookies.get(FLASH_COOKIE)?.value;
  if (flash) headers.set("x-flash", flash);

  const res = NextResponse.next({ request: { headers } });
  res.headers.set("Content-Security-Policy", csp);
  if (newCsrf) {
    res.cookies.set({ name: CSRF_COOKIE, value: newCsrf, path: "/", secure: true, sameSite: "strict", httpOnly: false });
  }
  if (flash) res.cookies.set({ name: FLASH_COOKIE, value: "", path: "/", maxAge: 0, httpOnly: true, secure: true, sameSite: "lax" });
  return res;
}

export const config = {
  matcher: [
    {
      source: "/((?!_next/static|_next/image|static/|healthz|favicon.ico|apple-touch-icon.png).*)",
      missing: [{ type: "header", key: "next-router-prefetch" }],
    },
  ],
};

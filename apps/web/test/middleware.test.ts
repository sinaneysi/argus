import { NextRequest } from "next/server";
import { describe, expect, it } from "vitest";
import { cspFor, middleware } from "@/middleware";

describe("middleware", () => {
  it("sets a per-request nonce CSP, frame-ancestors none, and the CSRF cookie once", () => {
    const a = middleware(new NextRequest("https://argus.example/today"));
    const b = middleware(new NextRequest("https://argus.example/today"));
    const csp = a.headers.get("content-security-policy")!;
    expect(csp).toMatch(/script-src 'self' 'nonce-[A-Za-z0-9+/=]+'/);
    expect(csp).toContain("frame-ancestors 'none'");
    expect(csp).toContain("default-src 'none'");
    expect(csp).not.toContain("unsafe-inline");
    expect(csp).not.toBe(b.headers.get("content-security-policy"));
    const set = a.cookies.get("argus_csrf");
    expect(set?.value).toMatch(/^[a-f0-9]{48}$/);
    expect(set?.httpOnly).toBeFalsy();
    expect(set?.sameSite).toBe("strict");
    const c = middleware(new NextRequest("https://argus.example/today", { headers: { cookie: "argus_csrf=" + "a".repeat(32) } }));
    expect(c.cookies.get("argus_csrf")).toBeUndefined();
  });
  it("moves the flash cookie into the request and clears it", () => {
    const r = middleware(new NextRequest("https://argus.example/review", { headers: { cookie: "argus_flash=x" } }));
    expect(r.cookies.get("argus_flash")?.value).toBe("");
    expect(r.headers.get("x-middleware-request-x-flash")).toBe("x");
  });
  it("keeps the CSP strict outside development", () => {
    expect(cspFor("n")).toContain("form-action 'self'");
    expect(cspFor("n")).toContain("base-uri 'none'");
  });
});

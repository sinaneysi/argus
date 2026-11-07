import { describe, expect, it, vi } from "vitest";
import { apiCall, errorOf, forwardHeaders, qs } from "@/lib/api";
import { parseSetCookie } from "@/lib/cookies";
import { minutes, safeNext } from "@/lib/format";

describe("api client", () => {
  it("forwards only the cookie, a sane address and the user agent", () => {
    const h = forwardHeaders(new Headers({ cookie: "opsr_session=abc", "x-forwarded-for": "198.51.100.7, 10.0.0.1",
      "user-agent": "UA", authorization: "Bearer nope", host: "evil.example" }));
    expect(h.get("cookie")).toBe("opsr_session=abc");
    expect(h.get("x-real-ip")).toBe("198.51.100.7");
    expect(h.get("user-agent")).toBe("UA");
    expect(h.get("authorization")).toBeNull();
    expect(h.get("host")).toBeNull();
    expect(forwardHeaders(new Headers({ "x-real-ip": "1.2.3.4 <script>" })).get("x-real-ip")).toBeNull();
  });

  it("posts JSON with the CSRF header and returns status, data and cookies", async () => {
    const fetchImpl = vi.fn(async (_url: string | URL | Request, init?: RequestInit) => {
      const h = new Headers(init?.headers);
      expect(h.get("x-csrf-token")).toBe("tok");
      expect(h.get("content-type")).toBe("application/json");
      expect(JSON.parse(String(init?.body))).toEqual({ sel: "ideas:1", action: "accept" });
      return new Response(JSON.stringify({ ok: true, redirect: "/review" }), {
        status: 200, headers: [["content-type", "application/json"], ["set-cookie", "a=1; Path=/; HttpOnly"]],
      });
    });
    process.env.API_URL = "http://api:8080/";
    const r = await apiCall<{ ok: boolean }>("POST", "/api/v1/review/act", new Headers(), {
      body: { sel: "ideas:1", action: "accept", reason: undefined }, csrf: "tok", fetchImpl: fetchImpl as unknown as typeof fetch });
    expect(fetchImpl.mock.calls[0]![0]).toBe("http://api:8080/api/v1/review/act");
    expect(r.status).toBe(200);
    expect(r.data.ok).toBe(true);
    expect(r.setCookies).toEqual(["a=1; Path=/; HttpOnly"]);
  });

  it("refuses anything but an API path", async () => {
    await expect(apiCall("GET", "//evil.example/x", new Headers())).rejects.toThrow();
    await expect(apiCall("GET", "/login", new Headers())).rejects.toThrow();
  });

  it("survives a non-JSON answer", async () => {
    const r = await apiCall("GET", "/api/v1/me", new Headers(), { fetchImpl: (async () => new Response("<html>", { status: 502 })) as unknown as typeof fetch });
    expect(r.status).toBe(502);
    expect(errorOf(r.data)).toMatch(/502/);
  });

  it("errors and query strings", () => {
    expect(errorOf({ error: "admins only" })).toBe("Admins only");
    expect(errorOf(null, "x")).toBe("x");
    expect(qs({ kind: "ideas", q: "", sel: "ideas:3", page: 2 })).toBe("?kind=ideas&sel=ideas:3&page=2");
    expect(qs({})).toBe("");
  });
});

describe("cookies and formatting", () => {
  it("parses Set-Cookie", () => {
    expect(parseSetCookie("opsr_session=t0k; Path=/; HttpOnly; Secure; SameSite=Lax; Max-Age=3600")).toEqual({
      name: "opsr_session", value: "t0k", path: "/", httpOnly: true, secure: true, sameSite: "lax", maxAge: 3600 });
    expect(parseSetCookie("=x")).toBeNull();
    expect(parseSetCookie("bad name=x")).toBeNull();
  });
  it("keeps redirects on this origin", () => {
    for (const bad of ["https://evil.example", "//evil.example", "/\\evil", "/auth/login", "", "/café"]) expect(safeNext(bad)).toBe("/today");
    expect(safeNext("/review?sel=ideas:1")).toBe("/review?sel=ideas:1");
  });
  it("formats minutes", () => {
    expect(minutes(45)).toBe("45 min");
    expect(minutes(120)).toBe("2 h");
    expect(minutes(130)).toBe("2 h 10 min");
  });
});

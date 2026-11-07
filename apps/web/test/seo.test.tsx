// Server-render tests of the Marketing → SEO page, from the API's real shape (captured from the demo data), its empty
// state, and Send posting the CSRF token to its endpoint.
import { prerenderToNodeStream } from "react-dom/static";
import { beforeEach, describe, expect, it, vi } from "vitest";
import seo from "./fixtures/seo.json";
import { noInlineStyle } from "./no-inline-style";

const answers: Record<string, { status: number; data: unknown }> = {};
const posted: { path: string; body: Record<string, unknown>; csrf: string }[] = [];
let answer: { status: number; data: unknown } = { status: 200, data: { ok: true, redirect: "/seo?sent=1" } };
const flashes: string[] = [];

vi.mock("@/lib/server", () => ({
  back: async (to: string) => { throw new Error("REDIRECT " + to); },
  apiGet: vi.fn(async (path: string) => ({ ...(answers[path.split("?")[0]!] ?? { status: 404, data: { error: "no fixture" } }), setCookies: [] })),
  apiPost: vi.fn(async (path: string, body: Record<string, unknown>, csrf: string) => { posted.push({ path, body, csrf }); return { ...answer, setCookies: [] }; }),
  csrfToken: vi.fn(async () => "csrf-token-0123456789"),
  readFlash: vi.fn(async () => null),
  field: (fd: FormData, k: string) => String(fd.get(k) ?? ""),
  formCsrf: (fd: FormData) => String(fd.get("csrf") ?? ""),
  setFlash: vi.fn(async (t: string) => { flashes.push(t); }),
}));
vi.mock("next/navigation", () => ({ usePathname: () => null,
  redirect: (to: string) => { throw new Error("REDIRECT " + to); },
  notFound: () => { throw new Error("NOT FOUND"); },
}));

beforeEach(() => {
  for (const k of Object.keys(answers)) delete answers[k];
  posted.length = 0;
  flashes.length = 0;
  answer = { status: 200, data: { ok: true, redirect: "/seo?sent=1" } };
});

async function html(el: Promise<React.ReactElement> | React.ReactElement) {
  const { prelude } = await prerenderToNodeStream(await el);
  let out = "";
  for await (const chunk of prelude) out += chunk.toString();
  return noInlineStyle(out.replace(/<!-- -->/g, ""));
}
const sp = (v: Record<string, string> = {}) => ({ searchParams: Promise.resolve(v) });

describe("SEO page", () => {
  it("shows the summary, suggestions with Send, every edge, the charts and Search Console", async () => {
    answers["/api/v1/seo"] = { status: 200, data: seo };
    const { default: Page } = await import("@/app/(app)/seo/page");
    const out = await html(Page(sp()));
    expect(out).toContain("<h1>SEO</h1>");
    expect(out).toContain('http-equiv="refresh" content="60"');
    for (const s of ["Operations summary", "Googlebot gets 503 on /category", "Top actions today", "Clicks falling", "Send to chat", "prod-edge-03", "cache edge", "Index state of key URLs", "Clicks per day"]) expect(out).toContain(s);
    expect(out).toContain('name="csrf" value="csrf-token-0123456789"');
    expect((out.match(/class="line-c"/g) ?? []).length).toBe(5);
    expect(out).not.toMatch(/\sstyle="/);
  });

  it("says what is missing on a fresh install", async () => {
    answers["/api/v1/seo"] = { status: 200, data: { empty: true, refresh: { seconds: 60 }, banners: [], summary: { findings: [], fix: 0 }, suggestions: { empty: true }, gsc: { empty: true } } };
    const { default: Page } = await import("@/app/(app)/seo/page");
    const out = await html(Page(sp()));
    for (const s of ["No Googlebot snapshot yet.", "No suggestions yet.", "No Search Console data yet.", "no action needed"]) expect(out).toContain(s);
    expect(out).not.toContain("Send to chat");
  });

  it("Send posts the CSRF token, and the demo refusal is a note", async () => {
    const a = await import("@/app/(app)/seo/actions");
    const fd = new FormData();
    fd.set("csrf", "c1");
    await expect(a.seoSend(fd)).rejects.toThrow("REDIRECT /seo?sent=1");
    expect(posted).toEqual([{ path: "/api/v1/seo/send", body: {}, csrf: "c1" }]);
    answer = { status: 403, data: { error: "Disabled in the demo" } };
    await expect(a.seoSend(fd)).rejects.toThrow("REDIRECT /seo");
    expect(flashes[0]).toContain("Disabled in the demo");
  });
});

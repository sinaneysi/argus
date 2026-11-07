// Settings → Data sources → Catalogue from the API's real shape, and its route decisions.
import { prerenderToNodeStream } from "react-dom/static";
import { beforeEach, describe, expect, it, vi } from "vitest";
import metrics from "./fixtures/catalogue-metrics.json";
import review from "./fixtures/catalogue-review.json";
import summary from "./fixtures/catalogue.json";
import { noInlineStyle } from "./no-inline-style";

const answers: Record<string, { status: number; data: unknown }> = {};
const posted: { path: string; body: Record<string, unknown> }[] = [];
const flashes: string[] = [];
let answer: { status: number; data: unknown } = { status: 202, data: { rid: "abc", state: "queued" } };

vi.mock("@/lib/server", () => ({
  back: async (to: string) => { throw new Error("REDIRECT " + to); },
  apiGet: vi.fn(async (path: string) => ({ ...(answers[path.split("?")[0]!] ?? { status: 404, data: {} }), setCookies: [], path })),
  apiPost: vi.fn(async (path: string, body: Record<string, unknown>) => { posted.push({ path, body }); return { ...answer, setCookies: [] }; }),
  csrfToken: vi.fn(async () => "csrf-token-0123456789"),
  readFlash: vi.fn(async () => null),
  field: (fd: FormData, k: string) => String(fd.get(k) ?? "").slice(0, 2000),
  formCsrf: (fd: FormData) => String(fd.get("csrf") ?? ""),
  setFlash: vi.fn(async (m: string) => { flashes.push(m); }),
}));
vi.mock("next/navigation", () => ({ usePathname: () => null, redirect: (to: string) => { throw new Error("REDIRECT " + to); }, notFound: () => { throw new Error("NOT FOUND"); } }));

beforeEach(() => {
  posted.length = 0;
  flashes.length = 0;
  answer = { status: 202, data: { rid: "abc", state: "queued" } };
  Object.assign(answers, {
    "/api/v1/sources/catalogue": { status: 200, data: summary }, "/api/v1/sources/review": { status: 200, data: review },
    "/api/v1/sources/metrics": { status: 200, data: metrics }, "/api/v1/me": { status: 200, data: { role: "admin" } },
  });
});

async function page(): Promise<string> {
  const { default: Page } = await import("@/app/(app)/admin/sources/catalogue/page");
  const { prelude } = await prerenderToNodeStream(await Page());
  const parts: Buffer[] = [];
  for await (const c of prelude) parts.push(Buffer.from(c as Uint8Array));
  return noInlineStyle(Buffer.concat(parts).toString("utf8").replace(/<!-- -->/g, ""));
}
const form = (o: Record<string, string>) => {
  const fd = new FormData();
  for (const [k, v] of Object.entries(o)) fd.append(k, v);
  return fd;
};

describe("Catalogue", () => {
  it("shows the verdict, the counts, the decisions, the sources and the metrics", async () => {
    const out = await page();
    const it0 = review.items[0]!;
    for (const w of ["<h1>Catalogue</h1>", summary.verdict.head, "Route decisions", it0.service, "Confirm", "Reject…", "Edit…", "Discover now", summary.sources[0]!.source, "Unmapped"]) expect(out).toContain(w);
  });
  it("offers a viewer no decision", async () => {
    answers["/api/v1/me"] = { status: 200, data: { role: "viewer" } };
    const out = await page();
    expect(out).toContain(review.items[0]!.service);
    expect(out).not.toContain("Discover now");
    expect(out).not.toContain("<form");
  });
  it("says when nothing waits", async () => {
    answers["/api/v1/sources/review"] = { status: 200, data: { ...review, items: [], total: 0 } };
    expect(await page()).toContain("No route waits for a decision.");
  });
  it("sends a decision through the review route and a discovery through its own", async () => {
    const { catalogueAct } = await import("@/app/(app)/admin/sources/catalogue/actions");
    await expect(catalogueAct(form({ action: "reject", id: "7", reason: "wrong", csrf: "t" }))).rejects.toThrow("REDIRECT /admin/sources/catalogue#decisions");
    expect(posted[0]).toEqual({ path: "/api/v1/sources/review/act", body: { action: "reject", id: "7", reason: "wrong", locator: undefined, note: undefined } });
    await expect(catalogueAct(form({ action: "discover", source: "gitlab/main", csrf: "t" }))).rejects.toThrow("REDIRECT");
    expect(posted[1]).toEqual({ path: "/api/v1/sources/catalogue/discover", body: { source: "gitlab/main" } });
  });
  it("refuses an unknown action, and names the demo refusal", async () => {
    const { catalogueAct } = await import("@/app/(app)/admin/sources/catalogue/actions");
    await expect(catalogueAct(form({ action: "drop", id: "7" }))).rejects.toThrow("REDIRECT");
    expect(posted).toEqual([]);
    answer = { status: 403, data: { error: "Disabled in the demo" } };
    await expect(catalogueAct(form({ action: "confirm", id: "7", csrf: "t" }))).rejects.toThrow("REDIRECT");
    expect(flashes[0]).toContain("Disabled in the demo");
  });
  it("sends a recent-2FA refusal to confirm", async () => {
    answer = { status: 403, data: { error: "two-factor authentication required", mfa: "stepup", sensitive: true } };
    const { catalogueAct } = await import("@/app/(app)/admin/sources/catalogue/actions");
    await expect(catalogueAct(form({ action: "confirm", id: "7", csrf: "t" }))).rejects.toThrow("REDIRECT /account/confirm");
  });
});

// Server-render tests of the help pages, from the API's real shapes (the admin's index and Settings page, a viewer's
// index and page): links inside the app, folds, admin blocks, search marks, and the refusals.
import { prerenderToNodeStream } from "react-dom/static";
import { beforeEach, describe, expect, it, vi } from "vitest";
import { Inline } from "@/components/Markdown";
import helpPage from "./fixtures/help-page.json";
import helpPageViewer from "./fixtures/help-page-viewer.json";
import helpSearch from "./fixtures/help-search.json";
import helpViewer from "./fixtures/help-viewer.json";
import help from "./fixtures/help.json";
import { noInlineStyle } from "./no-inline-style";

const answers: Record<string, { status: number; data: unknown }> = {};
const asked: string[] = [];

vi.mock("@/lib/server", () => ({
  back: async (to: string) => { throw new Error("REDIRECT " + to); },
  apiGet: vi.fn(async (path: string) => { asked.push(path); return { ...(answers[path.split("?")[0]!] ?? { status: 404, data: { error: "no" } }), setCookies: [] }; }),
}));
vi.mock("next/navigation", () => ({ useRouter: () => ({ refresh() {}, replace() {} }), usePathname: () => null, notFound: () => { throw new Error("NOT FOUND"); } }));

beforeEach(() => { for (const k of Object.keys(answers)) delete answers[k]; asked.length = 0; });

async function html(el: React.ReactNode): Promise<string> {
  const { prelude } = await prerenderToNodeStream(el);
  const parts: Buffer[] = [];
  for await (const c of prelude) parts.push(Buffer.from(c as Uint8Array));
  return noInlineStyle(Buffer.concat(parts).toString("utf8").replace(/<!-- -->/g, ""));
}
const sp = (o: Record<string, string> = {}) => Promise.resolve(o);
const params = (slug: string) => ({ params: Promise.resolve({ slug }) });

describe("/help", () => {
  it("shows the home text, one card per page and the search", async () => {
    answers["/api/v1/help"] = { status: 200, data: help };
    const { default: Page } = await import("@/app/(app)/help/page");
    const out = await html(await Page({ searchParams: sp() }));
    expect(out).toContain("<h1>Help</h1>");
    expect(out).toContain('action="/help"');
    expect(out).toContain('href="/help/settings"');
    expect(out).toContain("Admins");
    expect(out).not.toContain("](/");
  });
  it("gives a viewer no Settings card", async () => {
    answers["/api/v1/help"] = { status: 200, data: helpViewer };
    const { default: Page } = await import("@/app/(app)/help/page");
    expect(await html(await Page({ searchParams: sp() }))).not.toContain('href="/help/settings"');
  });
  it("marks search hits as text and passes the query on", async () => {
    answers["/api/v1/help"] = { status: 200, data: helpSearch };
    const { default: Page } = await import("@/app/(app)/help/page");
    const out = await html(await Page({ searchParams: sp({ q: "backup" }) }));
    expect(asked[0]).toBe("/api/v1/help?q=backup");
    expect(out).toContain("<mark>");
    expect(out).toContain("Clear the search");
    expect(out).not.toContain('class="h-cards"');
  });
  it("says so when nothing matches", async () => {
    answers["/api/v1/help"] = { status: 200, data: { q: "zzz", maxQuery: 100, results: { count: 0, shown: 0, rows: [] } } };
    const { default: Page } = await import("@/app/(app)/help/page");
    expect(await html(await Page({ searchParams: sp({ q: "zzz" }) }))).toContain("Nothing found for");
  });
});

describe("/help/<slug>", () => {
  it("renders sections, anchors, folds, admin blocks and links inside the app", async () => {
    answers["/api/v1/help/page"] = { status: 200, data: helpPage };
    const { default: Page } = await import("@/app/(app)/help/[slug]/page");
    const out = await html(await Page(params("settings")));
    expect(out).toContain(`<h1>${helpPage.doc.title}</h1>`);
    expect(out).toContain(`id="${helpPage.doc.sections[0]!.id}"`);
    expect(out).toContain('aria-current="page"');
    expect(out).toContain("/help/all");
    expect(out).toContain('class="h-fold fold-t"');
    expect(out).not.toContain("](/");
    expect(out).not.toContain(":::");
  });
  it("shows a viewer's page without an admin block", async () => {
    answers["/api/v1/help/page"] = { status: 200, data: helpPageViewer };
    const { default: Page } = await import("@/app/(app)/help/[slug]/page");
    expect(await html(await Page(params("review")))).not.toContain("For admins");
  });
  it("refuses an admin page for a viewer and 404s an unknown page", async () => {
    const { default: Page } = await import("@/app/(app)/help/[slug]/page");
    answers["/api/v1/help/page"] = { status: 403, data: { error: "This help page is for admins" } };
    expect(await html(await Page(params("settings")))).toContain("This help page is for admins");
    answers["/api/v1/help/page"] = { status: 404, data: { error: "no such help page" } };
    await expect(Page(params("nope"))).rejects.toThrow("NOT FOUND");
  });
});

describe("/help/all", () => {
  it("opens every fold and keeps the page anchors", async () => {
    answers["/api/v1/help/all"] = { status: 200, data: { docs: [helpPage.doc] } };
    const { default: Page } = await import("@/app/(app)/help/all/page");
    const out = await html(await Page());
    expect(out).toContain(`id="p-${helpPage.doc.slug}"`);
    expect(out).toMatch(/<details class="h-fold fold-t" open/);
  });
});

describe("Inline", () => {
  it("links inside the app only for help text, and only to a local path", async () => {
    expect(await html(<Inline text="see [Board](/board?x=1)" local />)).toContain('<a href="/board?x=1">Board</a>');
    expect(await html(<Inline text="see [Board](/board)" />)).not.toContain("<a");
    expect(await html(<Inline text="[x](//evil.example)" local />)).not.toContain("<a");
    expect(await html(<Inline text="[x](javascript:alert(1))" local />)).not.toContain("<a");
  });
});

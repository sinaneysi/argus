// Settings → Integrations from the API's real shape (captured as the admin), then its writes through the Settings actions.
import { prerenderToNodeStream } from "react-dom/static";
import { beforeEach, describe, expect, it, vi } from "vitest";
import outline from "./fixtures/admin-outline.json";
import view from "./fixtures/admin-outline-page.json";
import { noInlineStyle } from "./no-inline-style";

const answers: Record<string, { status: number; data: unknown }> = {};
const posted: { path: string; body: Record<string, unknown> }[] = [];
let answer: { status: number; data: unknown } = { status: 200, data: { ok: true, redirect: "/admin/outline?r=abc" } };

vi.mock("@/lib/server", () => ({
  back: async (to: string) => { throw new Error("REDIRECT " + to); },
  apiGet: vi.fn(async (path: string) => ({ ...(answers[path.split("?")[0]!] ?? { status: 404, data: {} }), setCookies: [], path })),
  apiPost: vi.fn(async (path: string, body: Record<string, unknown>) => { posted.push({ path, body }); return { ...answer, setCookies: [] }; }),
  csrfToken: vi.fn(async () => "csrf-token-0123456789"),
  readFlash: vi.fn(async () => null),
  field: (fd: FormData, k: string) => String(fd.get(k) ?? "").slice(0, 2000),
  formCsrf: (fd: FormData) => String(fd.get("csrf") ?? ""),
  setFlash: vi.fn(async () => undefined),
}));
vi.mock("next/navigation", () => ({ useRouter: () => ({ refresh() {}, replace() {} }), usePathname: () => null, redirect: (to: string) => { throw new Error("REDIRECT " + to); }, notFound: () => { throw new Error("NOT FOUND"); } }));

beforeEach(() => {
  for (const k of Object.keys(answers)) delete answers[k];
  posted.length = 0;
  answer = { status: 200, data: { ok: true, redirect: "/admin/outline?r=abc" } };
});

async function html(el: React.ReactNode): Promise<string> {
  const { prelude } = await prerenderToNodeStream(el);
  const parts: Buffer[] = [];
  for await (const c of prelude) parts.push(Buffer.from(c as Uint8Array));
  return noInlineStyle(Buffer.concat(parts).toString("utf8").replace(/<!-- -->/g, ""));
}
const render = async (d: unknown, status = 200) => {
  answers["/api/v1/admin/outline"] = { status, data: d };
  const { default: Page } = await import("@/app/(app)/admin/outline/page");
  return html(await Page({ searchParams: Promise.resolve({}) }));
};
const renderView = async (d: unknown, status = 200) => {
  answers["/api/v1/admin/outline/page"] = { status, data: d };
  const { default: Page } = await import("@/app/(app)/admin/outline/page/page");
  return html(await Page({ searchParams: Promise.resolve({ s: "shop/checkout" }) }));
};
const FAKE = "not-a-real-value"; // stands for a pasted key
const form = (o: Record<string, string>) => {
  const fd = new FormData();
  for (const [k, v] of Object.entries(o)) fd.append(k, v);
  return fd;
};

describe("Integrations", () => {
  it("shows Outline and GitLab with every control of a connected state", async () => {
    const out = await render(outline);
    for (const w of ["<h1>Integrations</h1>", 'id="outline"', 'id="gitlab"', "Connected as", "Sync now", "Disconnect Outline", "Disconnect GitLab", "Save mode and scope", "Allowed Outline hosts", "Allowed GitLab hosts", "Save defaults", "Use this project", "Board lists", "/admin/outline/page?s=shop%2Fcheckout", 'autoComplete="username"'.toLowerCase()])
      expect(out.toLowerCase()).toContain(w.toLowerCase());
    for (const m of out.matchAll(/<input[^>]*type="password"[^>]*>/g)) expect(m[0]).not.toContain("value=");
  });
  it("gives each connect form the hidden username field for password managers", async () => {
    const out = await render(outline);
    expect(out.match(/<input[^>]*name="who"[^>]*>/g)?.length).toBe(2);
    expect(out.match(/<input[^>]*name="who"[^>]*>/)?.[0]).toContain(`value="${outline.user}"`);
  });
  it("shows the first steps when nothing is connected, and no Publish yet", async () => {
    const out = await render({ ...outline, connection: undefined, connected: false, gitlab: { ...outline.gitlab, connected: false, connection: undefined } });
    for (const w of ["New API key", "Where to find the board URL and the token", "Connect first."]) expect(out).toContain(w);
    expect(out).not.toContain("Disconnect Outline");
    expect(out.match(/<input[^>]*value="publish"[^>]*>/)?.[0]).toContain("disabled");
  });
  it("is for admins", async () => {
    const out = await render({ error: "Admins only" }, 403);
    expect(out).toContain("Admins only");
    expect(out).not.toContain("<form");
  });
  it("sends each form to its own route and drops the username field", async () => {
    const { settingsAct } = await import("@/app/(app)/admin/settings");
    await expect(settingsAct(null, form({ _to: "outline", csrf: "t", action: "connect", token: FAKE, who: "demo.admin" }))).rejects.toThrow("REDIRECT /admin/outline?r=abc");
    expect(posted[0]).toEqual({ path: "/api/v1/admin/outline", body: { action: "connect", token: FAKE } });
    answer = { status: 200, data: { ok: true, redirect: "/admin/outline?g=abc#gitlab" } };
    await expect(settingsAct(null, form({ _to: "gitlab", csrf: "t", action: "sync" }))).rejects.toThrow("REDIRECT /admin/outline?g=abc#gitlab");
    expect(posted[1]?.path).toBe("/api/v1/admin/gitlab-issues");
  });
  it("never keeps a key after a refusal", async () => {
    answer = { status: 400, data: { error: "That does not look like a GitLab access token.", field: "token", form: "gitlab-connect" } };
    const { settingsAct } = await import("@/app/(app)/admin/settings");
    const st = await settingsAct(null, form({ _to: "gitlab", csrf: "t", action: "connect", project: "g/p", token: FAKE }));
    expect(st.field).toBe("token");
    expect(JSON.stringify(st)).not.toContain(FAKE);
    expect(st.values.project).toBe("g/p");
  });
});

describe("Outline page", () => {
  it("shows the facts, the changes, the page and its source", async () => {
    const out = await renderView(view);
    for (const w of [`<h1>${view.name}</h1>`, "Secret scan", "Changes since the previous build", "Page as Outline will show it", "Markdown sent to Outline", 'href="/admin/outline#pages"', 'class="add"', 'href="https://docs.northwind.example/doc/runbook"'])
      expect(out).toContain(w);
  });
  it("says when it is the first build", async () => {
    expect(await renderView({ ...view, diff: null })).toContain("First build of this page");
  });
  it("is not found for an unknown page, and for admins", async () => {
    await expect(renderView({ error: "no such page" }, 404)).rejects.toThrow("NOT FOUND");
    expect(await renderView({ error: "Admins only" }, 403)).toContain("Admins only");
  });
});

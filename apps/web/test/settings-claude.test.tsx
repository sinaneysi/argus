// Settings → Claude from the API's real shape (captured as the admin), then its writes through the Settings actions.
import { prerenderToNodeStream } from "react-dom/static";
import { beforeEach, describe, expect, it, vi } from "vitest";
import claude from "./fixtures/admin-claude.json";
import { noInlineStyle } from "./no-inline-style";

const answers: Record<string, { status: number; data: unknown }> = {};
const posted: { path: string; body: Record<string, unknown> }[] = [];
const flashes: string[] = [];
let answer: { status: number; data: unknown } = { status: 200, data: { ok: true, redirect: "/admin/claude?ct=1" } };

vi.mock("@/lib/server", () => ({
  back: async (to: string) => { throw new Error("REDIRECT " + to); },
  apiGet: vi.fn(async (path: string) => ({ ...(answers[path.split("?")[0]!] ?? { status: 404, data: {} }), setCookies: [] })),
  apiPost: vi.fn(async (path: string, body: Record<string, unknown>) => { posted.push({ path, body }); return { ...answer, setCookies: [] }; }),
  csrfToken: vi.fn(async () => "csrf-token-0123456789"),
  readFlash: vi.fn(async () => null),
  field: (fd: FormData, k: string) => String(fd.get(k) ?? "").slice(0, 2000),
  formCsrf: (fd: FormData) => String(fd.get("csrf") ?? ""),
  setFlash: vi.fn(async (m: string) => { flashes.push(m); }),
}));
vi.mock("next/navigation", () => ({ usePathname: () => null, redirect: (to: string) => { throw new Error("REDIRECT " + to); }, notFound: () => { throw new Error("NOT FOUND"); } }));

beforeEach(() => {
  for (const k of Object.keys(answers)) delete answers[k];
  posted.length = 0; flashes.length = 0;
  answer = { status: 200, data: { ok: true, redirect: "/admin/claude?ct=1" } };
});

async function html(el: React.ReactNode): Promise<string> {
  const { prelude } = await prerenderToNodeStream(el);
  const parts: Buffer[] = [];
  for await (const c of prelude) parts.push(Buffer.from(c as Uint8Array));
  return noInlineStyle(Buffer.concat(parts).toString("utf8").replace(/<!-- -->/g, ""));
}
const form = (o: Record<string, string>) => {
  const fd = new FormData();
  fd.set("csrf", "csrf-token-0123456789");
  for (const [k, v] of Object.entries(o)) fd.set(k, v);
  return fd;
};
const render = async (d: unknown, q: Record<string, string> = {}) => {
  answers["/api/v1/admin/claude"] = { status: 200, data: d };
  const { default: Page } = await import("@/app/(app)/admin/claude/page");
  return html(await Page({ searchParams: Promise.resolve(q) }));
};
const FAKE = "not-a-real-value"; // stands for a pasted credential

describe("Claude", () => {
  it("shows the status block, then token, guard and usage with their controls", async () => {
    const out = await render(claude);
    for (const w of ["<h1>Claude</h1>", 'id="claude-now"', "AI is working", "Test current token", "Replace the token", "sk-ant-oat01-", 'name="save_anyway"', "Replace and test", "Check now", "Guard settings", 'name="guard"', 'name="blocked"', 'name="fail"', "Claude usage", 'name="claude"'])
      expect(out).toContain(w);
    expect(out.match(/<input[^>]*name="token"[^>]*>/)?.[0]).not.toContain("value=");
  });
  it("opens the Replace fold when the token is missing, and shows a next action", async () => {
    const out = await render({ ...claude, status: { ...claude.status, kind: "fail", act: "Add a token", href: "#claude-replace" }, token: { ...claude.token, open: true } });
    expect(out).toContain('id="claude-replace" open=""');
    expect(out).toContain('href="#claude-replace"');
  });
  it("is for admins", async () => {
    answers["/api/v1/admin/claude"] = { status: 403, data: { error: "Admins only" } };
    const { default: Page } = await import("@/app/(app)/admin/claude/page");
    const out = await html(await Page({ searchParams: Promise.resolve({}) }));
    expect(out).toContain("Admins only");
    expect(out).not.toContain("<form");
  });
  it("offers to turn usage back on", async () => {
    const out = await render({ ...claude, usage: { on: false } });
    expect(out).toContain('name="claude" value="on"');
    expect(out).toContain("Turn on");
  });
  it("posts a replacement, and a refusal keeps no token", async () => {
    const { settingsAct } = await import("@/app/(app)/admin/settings");
    await expect(settingsAct(null, form({ _to: "claude-token", action: "replace", token: FAKE }))).rejects.toThrow("REDIRECT /admin/claude?ct=1");
    expect(posted[0]).toEqual({ path: "/api/v1/admin/claude-token", body: { action: "replace", token: FAKE } });
    answer = { status: 400, data: { error: "It starts with sk-ant-oat01-.", field: "token" } };
    const st = await settingsAct(null, form({ _to: "claude-token", action: "replace", token: FAKE, save_anyway: "1" }));
    expect(st).toMatchObject({ field: "token", values: { action: "replace", save_anyway: "1" } });
    expect(JSON.stringify(st)).not.toContain(FAKE);
  });
  it("checks the location through the token route and comes back to the guard, switches usage with its message, and sends a stale code to the step-up page", async () => {
    const { settingsGo } = await import("@/app/(app)/admin/settings");
    await expect(settingsGo(form({ _to: "claude-check", action: "egress" }))).rejects.toThrow("REDIRECT /admin/claude?ct=1");
    expect(posted[0]).toEqual({ path: "/api/v1/admin/claude-token", body: { action: "egress" } });
    answer = { status: 200, data: { ok: true, message: "Claude usage off. Applying now." } };
    await expect(settingsGo(form({ _to: "claude-usage", claude: "off" }))).rejects.toThrow("REDIRECT /admin/claude#claude-usage");
    expect(flashes[0]).toContain("Applying now");
    answer = { status: 403, data: { error: "two-factor authentication required", mfa: "stepup", sensitive: true } };
    await expect(settingsGo(form({ _to: "claude-token", action: "test" }))).rejects.toThrow(/REDIRECT \/account\/confirm/);
  });
});

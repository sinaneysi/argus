// Settings → Writing style: the style text in a textarea, Save and Reset to default through the Settings actions.
import { prerenderToNodeStream } from "react-dom/static";
import { beforeEach, describe, expect, it, vi } from "vitest";
import { noInlineStyle } from "./no-inline-style";

const answers: Record<string, { status: number; data: unknown }> = {};
const posted: { path: string; body: Record<string, unknown> }[] = [];
let answer: { status: number; data: unknown } = { status: 200, data: { ok: true, message: "Saved." } };

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

const API = "/api/v1/settings/writing-style";
const base = { text: "# Writing style\n\n- Write one idea in each sentence.\n", custom: false, line: "In use: the default style, shipped with Argus.", max: 16384, bytes: 52, notices: [], refresh: null, demo: false };

beforeEach(() => {
  for (const k of Object.keys(answers)) delete answers[k];
  posted.length = 0;
  answer = { status: 200, data: { ok: true, message: "Saved." } };
});

async function html(el: React.ReactNode): Promise<string> {
  const { prelude } = await prerenderToNodeStream(el);
  const parts: Buffer[] = [];
  for await (const c of prelude) parts.push(Buffer.from(c as Uint8Array));
  return noInlineStyle(Buffer.concat(parts).toString("utf8").replace(/<!-- -->/g, ""));
}
const render = async (d: unknown) => {
  answers[API] = { status: 200, data: d };
  const { default: Page } = await import("@/app/(app)/admin/writing-style/page");
  return html(await Page());
};

describe("Writing style", () => {
  it("shows the default style in a textarea, the tab and what uses it", async () => {
    const out = await render(base);
    for (const w of ["<h1>Writing style</h1>", 'aria-current="page"', 'id="style-text"', ">Default<", "In use: the default style", "task runs, RCA, proposals", 'name="_to" value="writing-style"', 'name="action" value="save"', "up to 16 KB", "Nothing to reset"])
      expect(out).toContain(w);
    const area = out.match(/<textarea[^>]*>[^<]*<\/textarea>/)?.[0] ?? "";
    for (const w of ['name="text"', 'maxLength="16384"', "required", "# Writing style\n\n- Write one idea in each sentence.\n</textarea>"]) expect(area).toContain(w);
    expect(out).not.toContain('value="reset"');
  });
  it("offers Reset to default for a custom style, confirmed where it sits", async () => {
    const out = await render({ ...base, custom: true, text: "Write short.\n", line: "In use: a custom style, saved by admin 2 min ago." });
    for (const w of [">Custom<", "saved by admin", "Reset to default…", 'name="action" value="reset"', "The custom text is deleted"]) expect(out).toContain(w);
  });
  it("is for admins", async () => {
    answers[API] = { status: 403, data: { error: "Admins only" } };
    const { default: Page } = await import("@/app/(app)/admin/writing-style/page");
    const out = await html(await Page());
    expect(out).toContain("Admins only");
    expect(out).not.toContain("<form");
  });
  it("saves the whole text, and a refusal keeps it with the field marked", async () => {
    const { settingsAct } = await import("@/app/(app)/admin/settings");
    const long = "Write short.\n".repeat(400); // over the 2000 characters other forms keep
    const fd = new FormData();
    fd.set("csrf", "csrf-token-0123456789"); fd.set("_to", "writing-style"); fd.set("action", "save"); fd.set("text", long);
    await expect(settingsAct(null, fd)).rejects.toThrow("REDIRECT /admin/writing-style");
    expect(posted[0]).toEqual({ path: "/api/v1/admin/writing-style", body: { action: "save", text: long } });
    answer = { status: 400, data: { error: "The text has control characters; plain text or Markdown only.", field: "text" } };
    const st = await settingsAct(null, fd);
    expect(st).toMatchObject({ field: "text", kind: "fail", values: { text: long } });
    answer = { status: 403, data: { error: "Disabled in the demo" } };
    expect(await settingsAct(null, fd)).toMatchObject({ kind: "info", error: "Disabled in the demo: nothing was sent and nothing changed." });
  });
});

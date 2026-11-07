// Settings → Data sources from the API's real shapes (captured as the admin), then its writes through the Settings actions.
import { prerenderToNodeStream } from "react-dom/static";
import { beforeEach, describe, expect, it, vi } from "vitest";
import add from "./fixtures/admin-sources-add.json";
import picker from "./fixtures/admin-sources-picker.json";
import sources from "./fixtures/admin-sources.json";
import { noInlineStyle } from "./no-inline-style";

const answers: Record<string, { status: number; data: unknown }> = {};
const posted: { path: string; body: Record<string, unknown> }[] = [];
let answer: { status: number; data: unknown } = { status: 200, data: { ok: true, redirect: "/admin/sources?sr=1" } };

vi.mock("@/lib/server", () => ({
  back: async (to: string) => { throw new Error("REDIRECT " + to); },
  apiGet: vi.fn(async (path: string) => ({ ...(answers[path.split("?")[0]!] ?? { status: 404, data: {} }), setCookies: [] })),
  apiPost: vi.fn(async (path: string, body: Record<string, unknown>) => { posted.push({ path, body }); return { ...answer, setCookies: [] }; }),
  csrfToken: vi.fn(async () => "csrf-token-0123456789"),
  readFlash: vi.fn(async () => null),
  field: (fd: FormData, k: string) => String(fd.get(k) ?? "").slice(0, 2000),
  formCsrf: (fd: FormData) => String(fd.get("csrf") ?? ""),
  setFlash: vi.fn(async () => undefined),
}));
vi.mock("next/navigation", () => ({ usePathname: () => null, redirect: (to: string) => { throw new Error("REDIRECT " + to); }, notFound: () => { throw new Error("NOT FOUND"); } }));

beforeEach(() => {
  for (const k of Object.keys(answers)) delete answers[k];
  posted.length = 0;
  answer = { status: 200, data: { ok: true, redirect: "/admin/sources?sr=1" } };
});

async function html(el: React.ReactNode): Promise<string> {
  const { prelude } = await prerenderToNodeStream(el);
  const parts: Buffer[] = [];
  for await (const c of prelude) parts.push(Buffer.from(c as Uint8Array));
  return noInlineStyle(Buffer.concat(parts).toString("utf8").replace(/<!-- -->/g, ""));
}
const render = async (d: unknown, q: Record<string, string> = {}) => {
  answers["/api/v1/admin/sources"] = { status: 200, data: d };
  const { default: Page } = await import("@/app/(app)/admin/sources/page");
  return html(await Page({ searchParams: Promise.resolve(q) }));
};
const FAKE = "not-a-real-value"; // stands for a pasted credential

describe("Data sources", () => {
  it("shows the status, a panel per type and every row's actions", async () => {
    const out = await render(sources);
    for (const w of ["<h1>Data sources</h1>", 'id="src-now"', "Add data source", "Check all now", 'id="src-t-gitlab"', 'id="src-gitlab-main"', "Edit…", "Remove main", "Manage in Settings → Kubernetes", 'name="action" value="check-all"', "credential ends"])
      expect(out).toContain(w);
    expect(out).not.toContain('id="src-add"');
  });
  it("offers no form for a managed row, and shows the operator's and the legacy ones", async () => {
    const p = sources.panels.find((x) => x.key === "kubernetes")!;
    expect(p.rows.every((r) => !r.canCheck)).toBe(true);
    const row = { ...sources.panels[0]!.rows[0]!, editable: undefined };
    const out = await render({ ...sources, panels: [{ key: "gitlab", label: "GitLab", managed: "", rows: [{ ...row, key: "gitlab/a", name: "a", operator: true }, { ...row, key: "gitlab/b", name: "b", change: "/admin/outline" }] }] });
    expect(out).toContain("Set by the operator");
    expect(out).toContain('href="/admin/outline"');
    expect(out).not.toContain("Remove a");
  });
  it("picks a type, with the managed one sent to its own page", async () => {
    const out = await render(picker);
    for (const w of ["Add a data source", "/admin/sources?add=grafana#src-add", "/admin/kube#kube-add", "Coming next"]) expect(out).toContain(w);
  });
  it("shows the add form of a type, with its username and no credential value", async () => {
    const out = await render(add);
    for (const w of ["Add Elasticsearch", 'name="url"', 'name="username"', 'name="credential"', 'name="env"', 'name="override"', 'name="purpose" value="agent"', "Save and check", "Choose another type"]) expect(out).toContain(w);
    expect(out.match(/<input[^>]*name="credential"[^>]*>/)?.[0]).not.toContain("value=");
    expect(out.match(/<input[^>]*name="credential"[^>]*>/)?.[0]).toContain("required");
  });
  it("edits with the name fixed, and an empty credential keeps the stored one", async () => {
    const out = await render({ ...add, block: "edit", form: { ...add.form, mode: "edit", name: "logs", env: "develop", last4: "ab12" } });
    expect(out).toContain("Edit Elasticsearch / OpenSearch logs");
    expect(out).toContain('type="hidden" name="name" value="logs"');
    expect(out).toContain("Leave empty to keep the stored one (ends …ab12)");
    expect(out.match(/<input[^>]*name="credential"[^>]*>/)?.[0]).not.toContain("required");
  });
  it("is for admins", async () => {
    answers["/api/v1/admin/sources"] = { status: 403, data: { error: "Admins only" } };
    const { default: Page } = await import("@/app/(app)/admin/sources/page");
    const out = await html(await Page({ searchParams: Promise.resolve({}) }));
    expect(out).toContain("Admins only");
    expect(out).not.toContain("<form");
  });
  it("posts a save, and a refusal keeps the entries but not the credential", async () => {
    const { settingsAct } = await import("@/app/(app)/admin/settings");
    const fd = () => {
      const f = new FormData();
      for (const [k, v] of Object.entries({ _to: "sources", action: "save", name: "east", url: "https://g.northwind.example", credential: FAKE })) f.set(k, v);
      return f;
    };
    await expect(settingsAct(null, fd())).rejects.toThrow("REDIRECT /admin/sources?sr=1");
    expect(posted[0]!.path).toBe("/api/v1/admin/sources");
    answer = { status: 400, data: { error: "https only.", field: "url" } };
    const st = await settingsAct(null, fd());
    expect(st).toMatchObject({ field: "url", values: { name: "east", url: "https://g.northwind.example" } });
    expect(JSON.stringify(st)).not.toContain(FAKE);
  });
});

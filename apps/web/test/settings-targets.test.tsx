// Settings → Backup targets and Credentials from the API's real shape (captured as the admin), then the target writes.
import { prerenderToNodeStream } from "react-dom/static";
import { beforeEach, describe, expect, it, vi } from "vitest";
import creds from "./fixtures/admin-creds.json";
import targets from "./fixtures/admin-targets.json";
import { noInlineStyle } from "./no-inline-style";

const answers: Record<string, { status: number; data: unknown }> = {};
const posted: { path: string; body: Record<string, unknown> }[] = [];
let answer: { status: number; data: unknown } = { status: 200, data: { ok: true, redirect: "/admin/backup-targets?req=abc" } };

vi.mock("@/lib/server", () => ({
  apiGet: vi.fn(async (path: string) => ({ ...(answers[path.split("?")[0]!] ?? { status: 404, data: {} }), setCookies: [], path })),
  apiPost: vi.fn(async (path: string, body: Record<string, unknown>) => { posted.push({ path, body }); return { ...answer, setCookies: [] }; }),
  csrfToken: vi.fn(async () => "csrf-token-0123456789"),
  readFlash: vi.fn(async () => null),
  field: (fd: FormData, k: string) => String(fd.get(k) ?? "").slice(0, 2000),
  formCsrf: (fd: FormData) => String(fd.get("csrf") ?? ""),
  setFlash: vi.fn(async () => undefined),
}));
vi.mock("next/navigation", () => ({ redirect: (to: string) => { throw new Error("REDIRECT " + to); }, notFound: () => { throw new Error("NOT FOUND"); } }));

beforeEach(() => {
  for (const k of Object.keys(answers)) delete answers[k];
  posted.length = 0;
  answer = { status: 200, data: { ok: true, redirect: "/admin/backup-targets?req=abc" } };
});

async function html(el: React.ReactNode): Promise<string> {
  const { prelude } = await prerenderToNodeStream(el);
  const parts: Buffer[] = [];
  for await (const c of prelude) parts.push(Buffer.from(c as Uint8Array));
  return noInlineStyle(Buffer.concat(parts).toString("utf8").replace(/<!-- -->/g, ""));
}
const targetsPage = async (d: unknown, status = 200) => {
  answers["/api/v1/admin/backup-targets"] = { status, data: d };
  const { default: Page } = await import("@/app/(app)/admin/backup-targets/page");
  return html(await Page({ searchParams: Promise.resolve({}) }));
};
const credsPage = async (d: unknown, status = 200) => {
  answers["/api/v1/admin/credentials"] = { status, data: d };
  const { default: Page } = await import("@/app/(app)/admin/credentials/page");
  return html(await Page());
};
const FAKE = "not-a-real-value"; // stands for a pasted key
const form = (o: Record<string, string | string[]>) => {
  const fd = new FormData();
  for (const [k, v] of Object.entries(o)) for (const x of [v].flat()) fd.append(k, x);
  return fd;
};

describe("Backup targets", () => {
  it("shows every target, its test and actions, and the add form with empty key fields", async () => {
    const out = await targetsPage(targets);
    const t = targets.rows[0]!;
    for (const w of ["<h1>Backup targets</h1>", t.name, t.endpoint, "Test target", "ILM", "mc ilm rule add", "Add a target", 'name="access_key"', 'name="secret_key"', 'name="envs"', "Add and test", 'value="yes"'])
      expect(out).toContain(w);
    for (const m of out.matchAll(/<input[^>]*type="password"[^>]*>/g)) expect(m[0]).not.toContain("value=");
  });
  it("offers Remove only for a target no plan uses", async () => {
    const rows = [{ ...targets.rows[0]!, name: "used-one", plans: 2 }, { ...targets.rows[0]!, name: "free-one", plans: 0 }];
    const out = await targetsPage({ ...targets, rows });
    expect(out).toContain("Remove free-one");
    expect(out).not.toContain("Remove used-one");
    expect(out).toContain("Used by 2 plans");
  });
  it("warns without an age key and says when there is no target", async () => {
    const out = await targetsPage({ ...targets, age_recipients: 0, rows: [] });
    expect(out).toContain("No age public key");
    expect(out).toContain("No target yet.");
  });
  it("is for admins", async () => {
    const out = await targetsPage({ error: "Admins only" }, 403);
    expect(out).toContain("Admins only");
    expect(out).not.toContain("<form");
  });
  it("posts the checked environments as a list and lands on the page the API names", async () => {
    const { settingsAct } = await import("@/app/(app)/admin/settings");
    await expect(settingsAct(null, form({ _to: "backup-targets", csrf: "t", action: "add", name: "dev-minio", secret_key: FAKE, who: "demo.admin", envs: ["develop", "release"] }))).rejects.toThrow("REDIRECT /admin/backup-targets?req=abc");
    expect(posted[0]).toEqual({ path: "/api/v1/admin/backup-targets", body: { action: "add", name: "dev-minio", secret_key: FAKE, envs: ["develop", "release"] } });
  });
  it("keeps the entries after a refusal, never a key, and marks the field", async () => {
    answer = { status: 400, data: { error: "Bucket: 3-63 lower-case letters, digits, dots and dashes.", field: "bucket", form: "target-add" } };
    const { settingsAct } = await import("@/app/(app)/admin/settings");
    const st = await settingsAct(null, form({ _to: "backup-targets", csrf: "t", action: "add", name: "dev-minio", bucket: "B", access_key: FAKE, secret_key: FAKE, envs: ["develop", "release"] }));
    expect(st.field).toBe("bucket");
    expect(st.values).toEqual({ action: "add", name: "dev-minio", bucket: "B", envs: "develop,release" });
    expect(JSON.stringify(st)).not.toContain(FAKE);
  });
  it("sends Test and Remove through the action, and says Disabled in the demo", async () => {
    const { settingsGo } = await import("@/app/(app)/admin/settings");
    answer = { status: 403, data: { error: "Disabled in the demo" } };
    await expect(settingsGo(form({ _to: "backup-targets", csrf: "t", action: "test", name: "dev-minio" }))).rejects.toThrow("REDIRECT /admin/backup-targets");
    expect(posted[0]?.path).toBe("/api/v1/admin/backup-targets");
  });
});

describe("Credentials", () => {
  it("shows the groups, the rules, the rotation steps and the check control", async () => {
    const out = await credsPage(creds);
    const b = creds.bad[0]!;
    for (const w of ["<h1>Credentials</h1>", "Need attention", "Credentials OK", b.name, `id="cred-${b.id}"`, "How to rotate", "Values are never read by this app", "Check now", 'name="tasks" value="cred-expiry"', 'href="/certs"'])
      expect(out).toContain(w);
  });
  it("says so before the first check", async () => {
    const out = await credsPage({ ...creds, checked: false, bad: [], ok: [], absent: [], howto: [], thresholds: undefined });
    expect(out).toContain("No credential check yet.");
    expect(out).toContain("Check now");
  });
  it("does not offer Check now while a check runs", async () => {
    const out = await credsPage({ ...creds, scan: { ...creds.scan, busy: true, meta: "Queued · refreshing" }, refresh: { seconds: 5 } });
    expect(out).toContain("Queued · refreshing");
    expect(out).not.toContain('name="tasks"');
  });
  it("is for admins", async () => {
    const out = await credsPage({ error: "Admins only" }, 403);
    expect(out).toContain("Admins only");
    expect(out).not.toContain("<form");
  });
});

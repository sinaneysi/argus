// Server-render tests of Settings → General and Users and access, from the API's real shapes (captured as the admin, so
// the write controls are on), then the writes through their server actions: refusals, step-up, the demo, the temporary password.
import { prerenderToNodeStream } from "react-dom/static";
import { beforeEach, describe, expect, it, vi } from "vitest";
import adminUsers from "./fixtures/admin-users.json";
import admin from "./fixtures/admin.json";
import { noInlineStyle } from "./no-inline-style";

const answers: Record<string, { status: number; data: unknown }> = {};
const posted: { path: string; body: Record<string, unknown>; csrf: string }[] = [];
const flashes: string[] = [];
let answer: { status: number; data: unknown } = { status: 200, data: { ok: true, message: "Saved. Applying now." } };

vi.mock("@/lib/server", () => ({
  back: async (to: string) => { throw new Error("REDIRECT " + to); },
  apiGet: vi.fn(async (path: string) => ({ ...(answers[path.split("?")[0]!] ?? { status: 404, data: {} }), setCookies: [] })),
  apiPost: vi.fn(async (path: string, body: Record<string, unknown>, csrf: string) => { posted.push({ path, body, csrf }); return { ...answer, setCookies: [] }; }),
  csrfToken: vi.fn(async () => "csrf-token-0123456789"),
  readFlash: vi.fn(async () => null),
  field: (fd: FormData, k: string) => String(fd.get(k) ?? "").slice(0, 2000),
  formCsrf: (fd: FormData) => String(fd.get("csrf") ?? ""),
  setFlash: vi.fn(async (m: string) => { flashes.push(m); }),
}));
vi.mock("next/navigation", () => ({ usePathname: () => null, redirect: (to: string) => { throw new Error("REDIRECT " + to); }, notFound: () => { throw new Error("NOT FOUND"); } }));
vi.mock("next/cache", () => ({ revalidatePath: vi.fn() }));

beforeEach(() => {
  for (const k of Object.keys(answers)) delete answers[k];
  posted.length = 0; flashes.length = 0;
  answer = { status: 200, data: { ok: true, message: "Saved. Applying now." } };
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

describe("General", () => {
  it("shows every section with its controls", async () => {
    answers["/api/v1/admin"] = { status: 200, data: admin };
    const { default: Page } = await import("@/app/(app)/admin/page");
    const out = await html(await Page());
    for (const w of ["<h1>General</h1>", "Claude usage", "Schedules", "Thresholds", "Automatic deep RCA", "Sign-in", "Save and apply", "Discard changes", "Credentials", "values are never shown", 'name="auth:sso_only"', 'name="rd:present"', 'name="qa:present"', `name="sched:${admin.schedules[0]!.task}"`, 'aria-current="page"']) expect(out).toContain(w);
  });
  it("is for admins", async () => {
    answers["/api/v1/admin"] = { status: 403, data: { error: "Admins only" } };
    const { default: Page } = await import("@/app/(app)/admin/page");
    const out = await html(await Page());
    expect(out).toContain("Admins only");
    expect(out).not.toContain("<form");
  });
  it("saves, then lands on the page with the answer", async () => {
    const { saveGeneral } = await import("@/app/(app)/admin/actions");
    await expect(saveGeneral(null, form({ "th:x": "5" }))).rejects.toThrow("REDIRECT /admin");
    expect(posted[0]).toEqual({ path: "/api/v1/admin/save", body: { "th:x": "5" }, csrf: "csrf-token-0123456789" });
    expect(flashes[0]).toContain("Applying now");
  });
  it("keeps the entries and names the field on a refusal, and in the demo", async () => {
    const { saveGeneral } = await import("@/app/(app)/admin/actions");
    answer = { status: 400, data: { error: "Too big.", field: "th:x" } };
    expect(await saveGeneral(null, form({ "th:x": "999" }))).toEqual({ error: "Not saved: Too big.", kind: "fail", field: "th:x", values: { "th:x": "999" } });
    answer = { status: 403, data: { error: "Disabled in the demo" } };
    expect(await saveGeneral(null, form({ "th:x": "1" }))).toMatchObject({ kind: "info", values: { "th:x": "1" } });
  });
  it("sends a change that needs a recent code to the step-up page", async () => {
    const { saveGeneral, setClaude } = await import("@/app/(app)/admin/actions");
    answer = { status: 403, data: { error: "two-factor authentication required", mfa: "stepup", sensitive: true } };
    await expect(saveGeneral(null, form({}))).rejects.toThrow(/REDIRECT \/account\/confirm/);
    await expect(setClaude(form({ claude: "off" }))).rejects.toThrow(/REDIRECT \/account\/confirm/);
  });
});

describe("Users and access", () => {
  it("shows the accounts, the actions on others and none on yourself", async () => {
    answers["/api/v1/admin/users"] = { status: 200, data: adminUsers };
    const { default: Page } = await import("@/app/(app)/admin/users/page");
    const out = await html(await Page());
    for (const w of ["<h1>Users and access</h1>", "(you)", "Two-factor authentication", "Matrix IDs", "Add user", 'name="matrix_id"', "Sign out everywhere", "Reset password", 'value="reset"']) expect(out).toContain(w);
    const me = adminUsers.rows.find((u) => u.you);
    if (me) expect(out).not.toContain(`value="${me.name}"><button`);
  });
  it("gives each row one Actions menu and each risky action its own confirm step", async () => {
    answers["/api/v1/admin/users"] = { status: 200, data: adminUsers };
    const { default: Page } = await import("@/app/(app)/admin/users/page");
    const out = await html(await Page());
    expect(out).not.toContain("<details class=\"fold u-menu");
    expect(out).toMatch(/popovertarget="u-menu-alex_doe"/i); // HTML attributes are case-insensitive
    expect(out).toContain('id="u-menu-alex_doe"');
    expect(out).toMatch(/popovertarget="u-reset-2fa-alex_doe"/i);
    // the 2FA reset step: a required reason, the new flow in words, Cancel, the action
    const step = out.slice(out.indexOf('id="u-reset-2fa-alex_doe"'));
    for (const w of ['name="reason"', "required", "You do not set up their new app.", "opens only the two-factor setup", 'popovertargetaction="hide"', 'value="reset-2fa"'])
      expect(step.slice(0, 2500).toLowerCase()).toContain(w.toLowerCase());
    expect(out).not.toMatch(/ style="/);
  });
  it("keeps the temporary password in the action's answer, never in the flash", async () => {
    const { userAct } = await import("@/app/(app)/admin/actions");
    answer = { status: 200, data: { ok: true, kind: "ok", message: "Added x. Temporary password, shown only now: abc" } };
    expect(await userAct(null, form({ action: "add", user: "x", role: "viewer" }))).toEqual({ text: "Added x. Temporary password, shown only now: abc", kind: "ok" });
    expect(flashes).toEqual([]);
    expect(posted[0]!.path).toBe("/api/v1/admin/users/act");
    expect(posted[0]!.body).toEqual({ action: "add", user: "x", role: "viewer" });
  });
  it("routes the Matrix form and shows refusals", async () => {
    const { userAct } = await import("@/app/(app)/admin/actions");
    answer = { status: 400, data: { error: "No such user." } };
    expect(await userAct(null, form({ user: "x", matrix_id: "" }))).toEqual({ text: "No such user.", kind: "fail" });
    expect(posted[0]!.path).toBe("/api/v1/admin/users/matrix");
    answer = { status: 200, data: { ok: false, kind: "fail", message: "Not allowed." } };
    expect(await userAct(null, form({ action: "disable", user: "me" }))).toEqual({ text: "Not allowed.", kind: "fail" });
    answer = { status: 403, data: { error: "Disabled in the demo" } };
    expect((await userAct(null, form({ action: "add", user: "x" }))).kind).toBe("info");
  });
  it("sends a change that needs a recent code to the step-up page", async () => {
    const { userAct } = await import("@/app/(app)/admin/actions");
    answer = { status: 403, data: { error: "two-factor authentication required", mfa: "stepup", sensitive: true } };
    await expect(userAct(null, form({ action: "mfa-login-on" }))).rejects.toThrow(/REDIRECT \/account\/confirm/);
  });
});

// Account and Two-factor from the API's real shape, then the writes. A seed or a recovery code lives only in the state of
// the action that made it, never in a cookie or a URL.
import { prerenderToNodeStream } from "react-dom/static";
import { beforeEach, describe, expect, it, vi } from "vitest";
import account from "./fixtures/account.json";
import mfa from "./fixtures/account-2fa.json";
import { noInlineStyle } from "./no-inline-style";

const answers: Record<string, { status: number; data: unknown }> = {};
const posted: { path: string; body: Record<string, unknown> }[] = [];
const flashes: string[] = [];
let answer: { status: number; data: unknown } = { status: 200, data: { ok: true, redirect: "/account" } };

vi.mock("@/lib/server", () => ({
  apiGet: vi.fn(async (path: string) => ({ ...(answers[path.split("?")[0]!] ?? { status: 404, data: {} }), setCookies: [], path })),
  apiPost: vi.fn(async (path: string, body: Record<string, unknown>) => { posted.push({ path, body }); return { ...answer, setCookies: [] }; }),
  csrfToken: vi.fn(async () => "csrf-token-0123456789"),
  readFlash: vi.fn(async () => null),
  field: (fd: FormData, k: string) => String(fd.get(k) ?? "").slice(0, 2000),
  formCsrf: (fd: FormData) => String(fd.get("csrf") ?? ""),
  setFlash: vi.fn(async (m: string) => { flashes.push(m); }),
}));
vi.mock("next/navigation", () => ({ redirect: (to: string) => { throw new Error("REDIRECT " + to); }, notFound: () => { throw new Error("NOT FOUND"); } }));

beforeEach(() => {
  for (const k of Object.keys(answers)) delete answers[k];
  posted.length = 0;
  flashes.length = 0;
  answer = { status: 200, data: { ok: true, redirect: "/account" } };
});

async function html(el: React.ReactNode): Promise<string> {
  const { prelude } = await prerenderToNodeStream(el);
  const parts: Buffer[] = [];
  for await (const c of prelude) parts.push(Buffer.from(c as Uint8Array));
  return noInlineStyle(Buffer.concat(parts).toString("utf8").replace(/<!-- -->/g, ""));
}
const page = async (d: unknown, status = 200) => {
  answers["/api/v1/account"] = { status, data: d };
  const { default: Page } = await import("@/app/(app)/account/page");
  return html(await Page({ searchParams: Promise.resolve({}) }));
};
const mfaPage = async (d: unknown, status = 200) => {
  answers["/api/v1/account/2fa"] = { status, data: d };
  const { default: Page } = await import("@/app/(app)/account/2fa/page");
  return html(await Page({ searchParams: Promise.resolve({}) }));
};
const form = (o: Record<string, string>) => {
  const fd = new FormData();
  for (const [k, v] of Object.entries(o)) fd.append(k, v);
  return fd;
};
const FAKE = "not-a-real-value";

describe("Account", () => {
  it("shows the password form, two-factor, sessions, Jira and Matrix", async () => {
    const out = await page(account);
    for (const w of ["<h1>Account</h1>", "Change password", 'name="current"', 'autoComplete="new-password"'.toLowerCase(), "Two-factor authentication", "Your sessions", 'id="jira"', 'id="matrix"', "Your Matrix ID"])
      expect(out.toLowerCase()).toContain(w.toLowerCase());
    for (const m of out.matchAll(/<input[^>]*type="password"[^>]*>/g)) expect(m[0]).not.toContain("value=");
  });
  it("gives the password and Jira token forms the hidden username field", async () => {
    const out = await page(account);
    expect(out.match(/<input[^>]*name="who"[^>]*>/g)?.length).toBeGreaterThanOrEqual(2);
  });
  it("asks only for the password while it must change", async () => {
    const out = await page({ user: "a", role: "viewer", source: "local", must_change: true, min_pw: 12 });
    expect(out).toContain("Choose a new password before continuing.");
    expect(out).not.toContain("Your sessions");
    expect(out).not.toContain('id="jira"');
  });
  it("has no password form for single sign-on", async () => {
    const out = await page({ ...account, source: "keycloak", sso: { issuer: "https://sso.northwind.example", client: "argus" } });
    expect(out).toContain("Single sign-on");
    expect(out).not.toContain('name="current"');
  });
  it("sends a gated session to enrolment", async () => {
    await expect(page({ error: "two-factor enrolment required", next: "/account/2fa" }, 403)).rejects.toThrow("REDIRECT /account/2fa");
  });
  it("never keeps a password after a refusal", async () => {
    answer = { status: 400, data: { error: "The new passwords do not match.", field: "new2" } };
    const { settingsAct } = await import("@/app/(app)/admin/settings");
    const st = await settingsAct(null, form({ _to: "account", csrf: "t", current: FAKE, new: FAKE, new2: FAKE + "2" }));
    expect(st.field).toBe("new2");
    expect(JSON.stringify(st)).not.toContain(FAKE);
  });
  it("signs a session out through the action, and the step-up sends it to confirm", async () => {
    const { settingsGo } = await import("@/app/(app)/admin/settings");
    answer = { status: 200, data: { ok: true, redirect: "/login" } };
    await expect(settingsGo(form({ _to: "account-sessions", csrf: "t", action: "revoke", id: "x" }))).rejects.toThrow("REDIRECT /login");
    expect(posted[0]).toEqual({ path: "/api/v1/account/sessions", body: { action: "revoke", id: "x" } });
    answer = { status: 403, data: { error: "two-factor authentication required", mfa: "stepup", next: "/account/2fa" } };
    await expect(settingsGo(form({ _to: "account-sessions", csrf: "t", action: "others" }))).rejects.toThrow("REDIRECT /account/confirm");
  });
});

describe("Two-factor", () => {
  it("offers setup when off", async () => {
    const out = await mfaPage({ ...mfa, enabled: false, key_ready: true });
    for (const w of ["Turn on two-factor authentication", 'name="password"', "Set up two-factor authentication"]) expect(out).toContain(w);
  });
  it("explains an admin's reset and that nothing else opens until setup", async () => {
    const out = await mfaPage({ ...mfa, enabled: false, key_ready: true, required: true, reset: true });
    for (const w of ["Set up two-factor authentication again", "An admin reset your two-factor authentication", "opens no other page", "reset by an admin", "Sign out", 'name="password"']) expect(out).toContain(w);
    expect(out).not.toContain(">Account</a>");
  });
  it("offers the three changes when on, with low recovery codes called out", async () => {
    const out = await mfaPage({ ...mfa, enabled: true, recovery_left: 2 });
    for (const w of ["Move to a new phone", "Make new recovery codes", "Turn off two-factor authentication", "2 of 10 recovery codes left"]) expect(out).toContain(w);
  });
  it("sends an old Keycloak sign-in to sign in again", async () => {
    const out = await mfaPage({ ...mfa, enabled: false, key_ready: true, local: false, reauth: "/auth/login?next=%2Faccount%2F2fa&reauth=1", auth_ago: "2 h ago" });
    expect(out).toContain("Sign in with Keycloak again");
    expect(out).not.toContain('name="password"');
  });
  it("says when the key file is missing and offers no setup", async () => {
    const out = await mfaPage({ ...mfa, enabled: false, key_ready: false });
    expect(out).toContain("the key file is missing");
    expect(out).not.toContain('name="password"');
  });
  it("keeps the seed in the action's answer only", async () => {
    const enrol = { key: "abcd efgh", uri: "otpauth://totp/x", account: "a", qr: { n: 21, d: "M0 0h1" }, ttl: 10 };
    answer = { status: 200, data: { ok: true, enrol, next: "/review" } };
    const { mfaAct } = await import("@/app/(app)/account/2fa/actions");
    const st = await mfaAct(null, form({ _to: "begin", csrf: "t", password: FAKE, next: "/review" }));
    expect(st.enrol).toEqual(enrol);
    expect(st.next).toBe("/review");
    expect(flashes).toEqual([]);
    expect(posted[0]).toEqual({ path: "/api/v1/account/2fa/begin", body: { password: FAKE, code: undefined, next: "/review" } });
  });
  it("returns the recovery codes in the answer, and a refusal keeps the step", async () => {
    const { mfaAct } = await import("@/app/(app)/account/2fa/actions");
    answer = { status: 200, data: { ok: true, codes: ["aaaa-bbbb"], title: "t", note: "n", made: "m", next: "/today" } };
    expect((await mfaAct(null, form({ _to: "confirm", csrf: "t", code: "123 456" }))).done?.codes).toEqual(["aaaa-bbbb"]);
    expect(posted[0]?.body.code).toBe("123456");
    expect(flashes).toEqual([]);
    const enrol = { key: "abcd", uri: "otpauth://totp/x", account: "a", qr: { n: 21, d: "M0 0h1" }, ttl: 10 };
    answer = { status: 400, data: { error: "That code is wrong or was already used.", enrol } };
    const st = await mfaAct(null, form({ _to: "confirm", csrf: "t", code: "000000" }));
    expect(st.error).toContain("wrong");
    expect(st.enrol).toEqual(enrol);
  });
  it("names the demo refusal, ignores an unknown form, and turns off through a flash", async () => {
    const { mfaAct } = await import("@/app/(app)/account/2fa/actions");
    expect((await mfaAct(null, form({ _to: "nope" }))).error).toBe("Unknown form.");
    expect(posted).toEqual([]);
    answer = { status: 403, data: { error: "Disabled in the demo" } };
    expect((await mfaAct(null, form({ _to: "begin", csrf: "t" }))).kind).toBe("info");
    answer = { status: 200, data: { ok: true, message: "off", redirect: "/account/2fa" } };
    await expect(mfaAct(null, form({ _to: "disable", csrf: "t", code: "123456" }))).rejects.toThrow("REDIRECT /account/2fa");
  });
});

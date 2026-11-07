// Server-render tests of the Delivery section and the step-up page, from the API's real shapes (fixtures captured from
// the demo data as the admin, so the write controls are on), plus what a viewer sees and every refusal of a write.
import { prerenderToNodeStream } from "react-dom/static";
import { beforeEach, describe, expect, it, vi } from "vitest";
import audit from "./fixtures/cicd-audit.json";
import failures from "./fixtures/cicd-failures.json";
import finding from "./fixtures/cicd-finding.json";
import project from "./fixtures/cicd-project.json";
import stuck from "./fixtures/cicd-stuck.json";
import cicd from "./fixtures/cicd.json";
import repoIndex from "./fixtures/repo-index.json";
import repo from "./fixtures/repo.json";
import group from "./fixtures/vuln-group.json";
import vulns from "./fixtures/vulns.json";
import { noInlineStyle } from "./no-inline-style";

const answers: Record<string, { status: number; data: unknown }> = {};
const asked: string[] = [];
const posted: { path: string; body: Record<string, unknown>; csrf: string }[] = [];
let answer: { status: number; data: unknown } = { status: 200, data: { ok: true, redirect: "/done" } };
const flashes: string[] = [];

vi.mock("@/lib/server", () => ({
  back: async (to: string) => { throw new Error("REDIRECT " + to); },
  apiGet: vi.fn(async (path: string) => { asked.push(path); return { ...(answers[path.split("?")[0]!] ?? { status: 404, data: { error: "no fixture" } }), setCookies: [] }; }),
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
  asked.length = 0;
  posted.length = 0;
  flashes.length = 0;
  answer = { status: 200, data: { ok: true, redirect: "/done" } };
});

async function html(el: Promise<React.ReactElement> | React.ReactElement) {
  const { prelude } = await prerenderToNodeStream(await el);
  let out = "";
  for await (const chunk of prelude) out += chunk.toString();
  return noInlineStyle(out.replace(/<!-- -->/g, ""));
}
const sp = (v: Record<string, string> = {}) => ({ searchParams: Promise.resolve(v) });
const form = (kv: Record<string, string>) => { const f = new FormData(); for (const [k, v] of Object.entries(kv)) f.set(k, v); return f; };
const clean = (out: string) => { expect(out).not.toMatch(/ style="/); expect(out).not.toMatch(/ on[a-z]+="/); };

describe("CI/CD pages", () => {
  it("shows the overview: attention, figures, environments, repo box, estate", async () => {
    answers["/api/v1/cicd"] = { status: 200, data: cicd };
    const { default: Page } = await import("@/app/(app)/cicd/page");
    const out = await html(Page(sp({ view: "all" })));
    expect(asked[0]).toBe("/api/v1/cicd?view=all");
    for (const s of ["<h1>CI/CD</h1>", "Green by environment", 'class="envbar"', 'id="matrix"', 'action="/cicd/repo"', "/cicd/project?id=", 'aria-label="CI/CD sections"']) expect(out).toContain(s);
    expect(out).toMatch(/<form class="filters"[^>]*action="\/cicd#matrix"/);
    clean(out);
  });
  it("names the environment filter and offers to clear it", async () => {
    answers["/api/v1/cicd"] = { status: 200, data: { ...cicd, pick: { count: 2, noun: "repos", env: "release", state: "failed", clear: "/cicd?view=all#matrix" } } };
    const { default: Page } = await import("@/app/(app)/cicd/page");
    const out = await html(Page(sp({ env: "release", state: "failed" })));
    expect(out).toContain("whose newest release pipeline is");
    expect(out).toContain('href="/cicd?view=all#matrix"');
  });
  it("lists failures with tabs, filters, and a task for an open one", async () => {
    answers["/api/v1/cicd/failures"] = { status: 200, data: failures };
    const { default: Page } = await import("@/app/(app)/cicd/failures/page");
    const out = await html(Page(sp()));
    expect(out).toContain("<h1>CI/CD failures</h1>");
    expect(out).toMatch(/<form class="filters"[^>]*action="\/cicd\/failures"/);
    expect(out).toContain("/cicd/finding?id=");
    expect(out).toContain("/board/");
    clean(out);
  });
  it("renders stuck, audit and project", async () => {
    answers["/api/v1/cicd/stuck"] = { status: 200, data: stuck };
    answers["/api/v1/cicd/audit"] = { status: 200, data: audit };
    answers["/api/v1/cicd/project"] = { status: 200, data: project };
    const s = await html((await import("@/app/(app)/cicd/stuck/page")).default());
    expect(s).toContain("Release deploys waiting at manual");
    const a = await html((await import("@/app/(app)/cicd/audit/page")).default());
    expect(a).toContain('id="security"');
    expect(a).toContain('id="variables"');
    const p = await html((await import("@/app/(app)/cicd/project/page")).default(sp({ id: "1" })));
    expect(p).toContain("Open findings");
    expect(p).toContain("Deploys");
    for (const o of [s, a, p]) clean(o);
  });
  it("shows a finding and answers 404 for an unknown one", async () => {
    answers["/api/v1/cicd/finding"] = { status: 200, data: finding };
    const { default: Page } = await import("@/app/(app)/cicd/finding/page");
    const out = await html(Page(sp({ id: "1" })));
    expect(out).toContain("Proposed fix");
    expect(out).toContain("Evidence");
    answers["/api/v1/cicd/finding"] = { status: 404, data: { error: "no such finding" } };
    await expect(Page(sp({ id: "9" }))).rejects.toThrow("NOT FOUND");
  });
});

describe("Check a repo", () => {
  it("lists checked repos and shows a bad link's error", async () => {
    answers["/api/v1/cicd/repo"] = { status: 200, data: { ...repoIndex, value: "x", error: "That is not a project link." } };
    const { default: Page } = await import("@/app/(app)/cicd/repo/page");
    const out = await html(Page(sp({ project: "x" })));
    expect(out).toContain("<h1>Check a repo</h1>");
    expect(out).toContain('role="alert"');
    expect(out).toContain("/cicd/repo?project=");
  });
  it("shows a report with its write control on", async () => {
    answers["/api/v1/cicd/repo"] = { status: 200, data: { ...repo, check: { can: true, busy: false, msg: "", label: "Re-check" } } };
    const { default: Page } = await import("@/app/(app)/cicd/repo/page");
    const out = await html(Page(sp({ project: "platform/x" })));
    for (const s of ["Pipelines per environment", 'name="csrf"', 'value="csrf-token-0123456789"', "Re-check"]) expect(out).toContain(s);
    clean(out);
  });
  it("disables the button and reloads while a check runs", async () => {
    answers["/api/v1/cicd/repo"] = { status: 200, data: { ...repo, refresh: true, check: { can: false, busy: true, msg: "queued", label: "Re-check" } } };
    const { default: Page } = await import("@/app/(app)/cicd/repo/page");
    const out = await html(Page(sp({ project: "platform/x" })));
    expect(out).toContain("Checking…");
    expect(out).toContain('http-equiv="refresh"');
    expect(out).not.toContain('name="csrf"');
  });
});

describe("Vulnerabilities", () => {
  it("shows the list, the filters and the coverage", async () => {
    answers["/api/v1/vulns"] = { status: 200, data: vulns };
    const { default: Page } = await import("@/app/(app)/vulns/page");
    const out = await html(Page(sp({ kev: "1" })));
    expect(asked[0]).toBe("/api/v1/vulns?kev=1");
    for (const s of ["<h1>Vulnerabilities</h1>", "Fix first", 'id="services"', 'id="components"', 'id="coverage"', 'name="host"']) expect(out).toContain(s);
    clean(out);
  });
  it("opens a group by group or id; the admin gets the suppress form, a viewer does not", async () => {
    answers["/api/v1/vulns/group"] = { status: 200, data: group };
    const { default: Page } = await import("@/app/(app)/vulns/page");
    const out = await html(Page(sp({ group: group.key })));
    for (const s of ['id="affected"', 'name="reason"', 'name="expires"', 'name="action" value="add"']) expect(out).toContain(s);
    clean(out);
    answers["/api/v1/vulns/group"] = { status: 200, data: { ...group, canSuppress: false, suppress: null } };
    const v = await html(Page(sp({ id: "5" })));
    expect(asked.at(-1)).toBe("/api/v1/vulns/group?id=5");
    expect(v).not.toContain('name="reason"');
    answers["/api/v1/vulns/group"] = { status: 404, data: { error: "no such component" } };
    await expect(Page(sp({ group: "bad" }))).rejects.toThrow("NOT FOUND");
  });
});

describe("Delivery writes", () => {
  it("posts a repo check and follows the answer", async () => {
    const a = await import("@/app/(app)/cicd/actions");
    answer = { status: 200, data: { ok: true, redirect: "/cicd/repo?project=a%2Fb&sent=1" } };
    await expect(a.repoCheck(form({ csrf: "c", project: "a/b" }))).rejects.toThrow("REDIRECT /cicd/repo?project=a%2Fb&sent=1");
    expect(posted[0]).toEqual({ path: "/api/v1/cicd/repo/check", body: { project: "a/b" }, csrf: "c" });
  });
  it("shows the demo refusal and a plain error on the page", async () => {
    const a = await import("@/app/(app)/cicd/actions");
    answer = { status: 403, data: { error: "Disabled in the demo" } };
    await expect(a.vulnSuppress(form({ csrf: "c", action: "remove", id: "1", back: "k|n|dev" }))).rejects.toThrow("REDIRECT /vulns?group=k%7Cn%7Cdev");
    expect(flashes[0]).toContain("Disabled in the demo");
    answer = { status: 400, data: { error: "The reason needs at least 20 characters." } };
    await expect(a.vulnSuppress(form({ csrf: "c", action: "add", reason: "short" }))).rejects.toThrow("REDIRECT /vulns");
    expect(flashes[1]).toBe("The reason needs at least 20 characters.");
    expect(posted[1]!.body).toMatchObject({ action: "add", reason: "short" });
  });
  it("sends a write that needs a code to the step-up page and back", async () => {
    const a = await import("@/app/(app)/cicd/actions");
    answer = { status: 403, data: { error: "two-factor authentication required", mfa: "stepup", sensitive: true } };
    await expect(a.vulnSuppress(form({ csrf: "c", action: "remove", id: "1", back: "k" }))).rejects.toThrow("REDIRECT /account/confirm?next=%2Fvulns%3Fgroup%3Dk&sensitive=1");
    answer = { status: 403, data: { error: "two-factor authentication required", mfa: "enrol" } };
    await expect(a.repoCheck(form({ csrf: "c", project: "a/b" }))).rejects.toThrow("REDIRECT /account/2fa?next=");
    expect(flashes).toEqual([]);
  });
  it("sends every other section's refused write there too", async () => {
    answer = { status: 403, data: { error: "two-factor authentication required", mfa: "stepup", sensitive: false } };
    const inc = await import("@/app/(app)/incidents/actions");
    await expect(inc.incidentAct(form({ csrf: "c", id: "7", action: "ack" }))).rejects.toThrow("REDIRECT /account/confirm?next=%2Fincidents%2Fview%3Fid%3D7");
    const rev = await import("@/app/(app)/review/actions");
    await expect(rev.reviewAct(form({ csrf: "c", sel: "x", action: "y", here: "/review?sel=x" }))).rejects.toThrow("REDIRECT /account/confirm?next=%2Freview%3Fsel%3Dx");
    const tod = await import("@/app/(app)/today/actions");
    await expect(tod.todayAct(form({ csrf: "c", action: "done", item: "1" }))).rejects.toThrow("REDIRECT /account/confirm?next=%2Ftoday");
  });
});

describe("step-up page", () => {
  const up = { state: "stepup", why: "Changes need a code from the last 15 minutes.", next: "/vulns", reauth: "" };
  it("asks for a code and offers a recovery code", async () => {
    answers["/api/v1/account/stepup"] = { status: 200, data: up };
    const { default: Page } = await import("@/app/(app)/account/confirm/page");
    const out = await html(Page(sp({ next: "/vulns", sensitive: "1" })));
    expect(asked[0]).toBe("/api/v1/account/stepup?next=%2Fvulns&sensitive=1");
    for (const s of ["Confirm it&#x27;s you", 'autoComplete="one-time-code"'.toLowerCase(), "recovery code", 'name="next" value="/vulns"', 'name="sensitive" value="1"']) expect(out.toLowerCase()).toContain(s.toLowerCase());
    clean(out);
  });
  it("offers the provider's sign-in, goes on when nothing is needed, and sends enrolment to its page", async () => {
    const { default: Page } = await import("@/app/(app)/account/confirm/page");
    answers["/api/v1/account/stepup"] = { status: 200, data: { ...up, reauth: "/auth/login?next=%2Fvulns&reauth=1" } };
    const out = await html(Page(sp({ next: "/vulns" })));
    expect(out).toContain("/auth/login?next=%2Fvulns&amp;reauth=1");
    expect(out).not.toContain('name="code"');
    answers["/api/v1/account/stepup"] = { status: 200, data: { ...up, state: "" } };
    await expect(Page(sp({ next: "/vulns" }))).rejects.toThrow("REDIRECT /vulns");
    answers["/api/v1/account/stepup"] = { status: 200, data: { ...up, state: "enrol" } };
    await expect(Page(sp({ next: "/vulns" }))).rejects.toThrow("REDIRECT /account/2fa?next=%2Fvulns");
  });
  it("never leaves the site, and keeps the visitor here on a wrong code", async () => {
    const { default: Page } = await import("@/app/(app)/account/confirm/page");
    answers["/api/v1/account/stepup"] = { status: 200, data: { ...up, state: "" } };
    await expect(Page(sp({ next: "//evil.test/" }))).rejects.toThrow("REDIRECT /today");
    const a = await import("@/app/(app)/account/confirm/actions");
    answer = { status: 400, data: { error: "That code is wrong or was already used." } };
    await expect(a.stepUpAct(form({ csrf: "c", code: "000 000", next: "/vulns", sensitive: "1" }))).rejects.toThrow("REDIRECT /account/confirm?next=%2Fvulns&sensitive=1");
    expect(posted[0]!.body).toEqual({ code: "000000", next: "/vulns" });
    expect(flashes[0]).toContain("wrong");
    answer = { status: 200, data: { ok: true, redirect: "/vulns" } };
    await expect(a.stepUpAct(form({ csrf: "c", code: "123456", next: "/vulns" }))).rejects.toThrow("REDIRECT /vulns");
  });
});

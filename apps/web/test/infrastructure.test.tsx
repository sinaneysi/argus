// Server-render tests of the Infrastructure section, from the API's real shapes (fixtures captured from the demo data as
// the admin, so the write controls are on), plus the variants a viewer and each step of a form see.
import { prerenderToNodeStream } from "react-dom/static";
import { beforeEach, describe, expect, it, vi } from "vitest";
import backupNew from "./fixtures/backup-new.json";
import backupPlan from "./fixtures/backup-plan.json";
import backupSet from "./fixtures/backup-set.json";
import backups from "./fixtures/backups.json";
import capacity from "./fixtures/capacity.json";
import certs from "./fixtures/certs.json";
import checks from "./fixtures/checks.json";
import hygiene from "./fixtures/hygiene.json";
import inventory from "./fixtures/inventory.json";
import vm from "./fixtures/inventory-item.json";
import { noInlineStyle } from "./no-inline-style";

const answers: Record<string, { status: number; data: unknown }> = {};
const posted: { path: string; body: Record<string, unknown>; csrf: string }[] = [];
let answer: { status: number; data: unknown } = { status: 200, data: { ok: true, redirect: "/done" } };
const flashes: string[] = [];

vi.mock("@/lib/server", () => ({
  back: async (to: string) => { throw new Error("REDIRECT " + to); },
  apiGet: vi.fn(async (path: string) => ({ ...(answers[path.split("?")[0]!] ?? { status: 404, data: { error: "no fixture" } }), setCookies: [] })),
  apiPost: vi.fn(async (path: string, body: Record<string, unknown>, csrf: string) => { posted.push({ path, body, csrf }); return { ...answer, setCookies: [] }; }),
  csrfToken: vi.fn(async () => "csrf-token-0123456789"),
  readFlash: vi.fn(async () => null),
  field: (fd: FormData, k: string) => String(fd.get(k) ?? ""),
  formCsrf: (fd: FormData) => String(fd.get("csrf") ?? ""),
  setFlash: vi.fn(async (t: string) => { flashes.push(t); }),
}));
vi.mock("next/navigation", () => ({ useRouter: () => ({ refresh() {}, replace() {} }), usePathname: () => null,
  redirect: (to: string) => { throw new Error("REDIRECT " + to); },
  notFound: () => { throw new Error("NOT FOUND"); },
}));

beforeEach(() => {
  for (const k of Object.keys(answers)) delete answers[k];
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

describe("infrastructure pages", () => {
  it("lists VMs with facets, CSV and Scan now", async () => {
    answers["/api/v1/inventory"] = { status: 200, data: inventory };
    const { default: Page } = await import("@/app/(app)/inventory/page");
    const out = await html(Page(sp()));
    expect(out).toContain("<h1>Inventory</h1>");
    expect(out).toMatch(/<form class="filters"[^>]*action="\/inventory"/);
    expect(out).toContain('href="/inventory/view?id=');
    expect(out).toContain('href="/inventory.csv"');
    expect(out).toContain("Scan now");
    expect(out).not.toMatch(/\sstyle="/);
  });

  it("the search box goes straight to the one matching VM", async () => {
    answers["/api/v1/inventory"] = { status: 200, data: { goto: "/inventory/view?id=7" } };
    const { default: Page } = await import("@/app/(app)/inventory/page");
    await expect(Page(sp({ q: "shop", go: "1" }))).rejects.toThrow("REDIRECT /inventory/view?id=7");
  });

  it("shows a VM with every section, and Analyse for an admin only", async () => {
    answers["/api/v1/inventory/item"] = { status: 200, data: vm };
    const { default: Page } = await import("@/app/(app)/inventory/view/page");
    const out = await html(Page(sp({ id: String(vm.id) })));
    for (const id of ["services", "upstreams", "analysis", "vulns", "resources", "filesystems", "history", "record"]) expect(out).toContain(`id="${id}"`);
    expect(out).toContain('name="what" value="refresh"');
    expect(out).toContain('name="what" value="analyse"');
    expect(out).toContain('class="mini"');
    expect(out).not.toMatch(/\sstyle="/);
    answers["/api/v1/inventory/item"] = { status: 200, data: { ...vm, actions: { ...vm.actions, analyse: null } } };
    expect(await html(Page(sp({ id: "1" })))).not.toContain('value="analyse"');
    answers["/api/v1/inventory/item"] = { status: 404, data: {} };
    await expect(Page(sp({ id: "9" }))).rejects.toThrow("NOT FOUND");
  });

  it("capacity keeps the filesystems' Show choice in the VM filter and uses its own sort key", async () => {
    answers["/api/v1/capacity"] = { status: 200, data: { ...capacity, list: { ...capacity.list, hidden: { level: "all" } } } };
    const { default: Page } = await import("@/app/(app)/capacity/page");
    const out = await html(Page(sp({ level: "all" })));
    expect(out).toContain('name="level" value="all"');
    expect(out).toContain('name="vsort"');
    expect(out).toContain('id="vms"');
    expect(out).toContain('name="fs"');
  });

  it("certificates, hygiene and checks render their sections", async () => {
    answers["/api/v1/certs"] = { status: 200, data: certs };
    answers["/api/v1/hygiene"] = { status: 200, data: hygiene };
    answers["/api/v1/checks"] = { status: 200, data: checks };
    const c = await html((await import("@/app/(app)/certs/page")).default());
    expect(c).toContain("By certificate");
    const h = await html((await import("@/app/(app)/hygiene/page")).default());
    expect(h).toContain("Suggestions only");
    const k = await html((await import("@/app/(app)/operations/page")).default());
    expect(k).toContain('id="checks"');
    expect(k).toContain('class="strip"');
    expect(k).toContain("Argus health");
    expect(k).toContain('name="tasks" value="all"');
    expect(k).toContain('name="tasks" value="backup-check"');
  });

  it("backups shows sets, coverage and plans; a viewer's New backup is quiet", async () => {
    answers["/api/v1/backups"] = { status: 200, data: backups };
    const { default: Page } = await import("@/app/(app)/backups/page");
    const out = await html(Page(sp()));
    expect(out).toContain('id="coverage"');
    expect(out).toContain('id="plans"');
    expect(out).toContain("Mark as OK");
    expect(out).toMatch(/class="[^"]*bg-primary[^"]*" href="\/backups\/new"/);
    answers["/api/v1/backups"] = { status: 200, data: { ...backups, admin: false } };
    expect(await html(Page(sp()))).toMatch(/class="[^"]*border-border[^"]*" href="\/backups\/new"/);
  });

  it("a failing set offers Mark as OK with the three scopes; an accepted one offers Undo", async () => {
    const override = { check: "STALE", openDays: 30, maxDays: 30 };
    answers["/api/v1/backups/set"] = { status: 200, data: { ...backupSet, override, accepted: null } };
    const { default: Page } = await import("@/app/(app)/backups/set/page");
    const out = await html(Page(sp({ name: backupSet.name })));
    for (const s of ["until-ok", "days", "until-changed"]) expect(out).toContain(`value="${s}"`);
    expect(out).toContain('name="check" value="STALE"');
    expect(out).toContain('class="line-c"');
    answers["/api/v1/backups/set"] = { status: 200, data: { ...backupSet, override: null, accepted: { id: 4, by: "alex.doe", scope: "until it passes", reason: "Planned move", canUndo: true } } };
    const acc = await html(Page(sp({ name: backupSet.name })));
    expect(acc).toContain('name="action" value="undo"');
    expect(acc).toContain('name="id" value="4"');
  });

  it("New backup walks test, testing, failed, tested, and never shows the string", async () => {
    const test = { state: "", member: "", reason: "", missing: "", facts: [], absent: [] };
    answers["/api/v1/backups/new"] = { status: 200, data: { ...backupNew, admin: true, test } };
    const { default: Page } = await import("@/app/(app)/backups/new/page");
    let out = await html(Page(sp()));
    expect(out).toContain('type="password"');
    expect(out).toContain('value="test" name="action"');
    expect(out).toMatch(/value="preview"/);
    answers["/api/v1/backups/new"] = { status: 200, data: { ...backupNew, admin: true, draft: "0123456789abcdef", refresh: { seconds: 3 }, test: { ...test, state: "testing", member: "db-1" } } };
    out = await html(Page(sp({ draft: "0123456789abcdef" })));
    expect(out).toContain("Testing…");
    expect(out).toContain('http-equiv="refresh"');
    answers["/api/v1/backups/new"] = { status: 200, data: { ...backupNew, admin: true, test: { ...test, state: "fail", reason: "login refused", missing: "GRANT backup" } } };
    out = await html(Page(sp()));
    expect(out).toContain("Test again");
    expect(out).toContain("GRANT backup");
    answers["/api/v1/backups/new"] = { status: 200, data: { ...backupNew, admin: true, tested: true, draft: "0123456789abcdef", current: { uriRedacted: "mongodb://***@db-1", sha: "abc", token: "tok" }, test: { ...test, state: "ok" } } };
    out = await html(Page(sp({ draft: "0123456789abcdef" })));
    expect(out).toContain('value="save" name="action"');
    expect(out).toContain('name="token" value="tok"');
    expect(out).not.toContain('type="password"');
    answers["/api/v1/backups/new"] = { status: 200, data: { ...backupNew, admin: false, test } };
    expect(await html(Page(sp()))).toContain("Admins test and save backups.");
  });

  it("a plan offers Install now, records restore tests, and a production plan shows the operator's command", async () => {
    answers["/api/v1/backups/plan"] = { status: 200, data: { ...backupPlan, prod: false, installBtn: { label: "Install now", busy: false, again: false }, testForm: { today: "2026-11-04" }, install: { mode: "none", server: "db-1" } } };
    const { default: Page } = await import("@/app/(app)/backups/plan/page");
    let out = await html(Page(sp({ id: "1" })));
    expect(out).toContain("Install now");
    expect(out).toContain('name="plan"');
    expect(out).toContain("Record restore test");
    answers["/api/v1/backups/plan"] = { status: 200, data: { ...backupPlan, prod: true, installBtn: null, testForm: null, install: { mode: "operator", command: "ssh argus-01 bundle", after: "x", host: "argus-01", installedAt: null } } };
    out = await html(Page(sp({ id: "1" })));
    expect(out).toContain("Production change: needs");
    expect(out).not.toContain("banner fail");
    expect(out).toContain("ssh argus-01 bundle");
    expect(out).not.toContain("Record restore test");
  });
});

describe("infrastructure writes", () => {
  it("each form posts its fields and the CSRF token to its endpoint", async () => {
    const a = await import("@/app/(app)/inventory/actions");
    const go = async (p: Promise<void>) => { await expect(p).rejects.toThrow("REDIRECT /done"); };
    await go(a.scanAct(form({ csrf: "c1", tasks: "all", back: "/operations" })));
    await go(a.vmAct(form({ csrf: "c1", id: "7", what: "analyse" })));
    await go(a.vmAct(form({ csrf: "c1", id: "7", what: "anything" })));
    await go(a.capacityAnalyse(form({ csrf: "c1", fs: "0123456789ab" })));
    await go(a.backupInstall(form({ csrf: "c1", plan: "3" })));
    await go(a.backupRestore(form({ csrf: "c1", plan: "3", date: "2026-11-04", result: "ok", object: "o", notes: "n" })));
    await go(a.backupOverride(form({ csrf: "c1", action: "add", set: "s", check: "STALE", scope: "days", days: "7", reason: "because it moved", back: "/backups/set?name=s" })));
    expect(posted.map((p) => p.path)).toEqual(["/api/v1/scan", "/api/v1/inventory/analyse", "/api/v1/inventory/refresh", "/api/v1/capacity/analyse", "/api/v1/backups/install", "/api/v1/backups/restore", "/api/v1/backups/override"]);
    expect(posted.every((p) => p.csrf === "c1")).toBe(true);
    expect(posted[6]!.body).toMatchObject({ action: "add", set: "s", scope: "days", days: "7" });
  });

  it("New backup sends the string once and shows the errors the API returns", async () => {
    const a = await import("@/app/(app)/inventory/actions");
    answer = { status: 200, data: { ok: true, redirect: "/backups/new?databases=shop", errors: ["Connection string: no host."] } };
    const uri = "mongodb://u:" + "p" + "@";
    await expect(a.backupNew(form({ csrf: "c1", action: "test", uri, databases: "shop" }))).rejects.toThrow("REDIRECT /backups/new?databases=shop");
    expect(posted[0]!.body).toMatchObject({ action: "test", uri, databases: "shop" });
    expect(flashes[0]).toBe("Not yet: Connection string: no host.");
  });

  it("a refusal comes back as words: the demo, a two-factor step, a role", async () => {
    const a = await import("@/app/(app)/inventory/actions");
    answer = { status: 403, data: { error: "Disabled in the demo" } };
    await expect(a.scanAct(form({ csrf: "c", tasks: "cve", back: "/certs" }))).rejects.toThrow("REDIRECT /certs");
    expect(flashes[0]).toContain("Disabled in the demo");
    answer = { status: 403, data: { error: "two-factor authentication required", mfa: "stepup" } };
    await expect(a.backupOverride(form({ csrf: "c", action: "undo", id: "1", back: "//evil.test/" }))).rejects.toThrow("REDIRECT /account/confirm?next=%2Fbackups");
    expect(flashes.length).toBe(1);
    answer = { status: 403, data: { error: "Only admins can start a VM analysis." } };
    await expect(a.vmAct(form({ csrf: "c", id: "7", what: "analyse" }))).rejects.toThrow("REDIRECT /inventory/view?id=7");
    expect(flashes[1]).toBe("Only admins can start a VM analysis.");
  });

  it("shows the broker's commands on a VM, and nothing with no broker", async () => {
    const { Connect } = await import("@/components/Connect");
    const steps = [{ label: "Open a shell", cmd: "tsh ssh ops@web-01" }];
    const out = await html(Promise.resolve(<Connect id="connect" steps={steps} via="Teleport" />));
    expect(out).toContain("Connect with Teleport");
    expect(out).toContain("tsh ssh ops@web-01");
    expect(await html(Promise.resolve(<Connect id="connect" steps={null} via="" />))).toBe("");
  });
});

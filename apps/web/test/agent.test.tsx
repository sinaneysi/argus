// Server-render tests of the Agent section, from the API's real shapes (fixtures captured from the demo data as the
// admin, so the write control is on), plus what a viewer gets and every refusal of Take action.
import { prerenderToNodeStream } from "react-dom/static";
import { beforeEach, describe, expect, it, vi } from "vitest";
import agentRun from "./fixtures/agent-run.json";
import fixReviewProd from "./fixtures/fix-review-prod.json";
import fixReview from "./fixtures/fix-review.json";
import fixRun from "./fixtures/fix-run.json";
import fix from "./fixtures/fix.json";
import knowledge from "./fixtures/knowledge.json";
import runs from "./fixtures/runs.json";
import { noInlineStyle } from "./no-inline-style";

const answers: Record<string, { status: number; data: unknown }> = {};
const asked: string[] = [];
const posted: { path: string; body: Record<string, unknown>; csrf: string }[] = [];
let answer: { status: number; data: unknown } = { status: 200, data: { ok: true, redirect: "/admin/fix/run?id=1" } };

vi.mock("@/components/NotConfigured", () => ({ notConfigured: async () => null }));
vi.mock("@/lib/server", () => ({
  back: async (to: string) => { throw new Error("REDIRECT " + to); },
  apiGet: vi.fn(async (path: string) => { asked.push(path); return { ...(answers[path.split("?")[0]!] ?? { status: 404, data: { error: "no fixture" } }), setCookies: [] }; }),
  apiPost: vi.fn(async (path: string, body: Record<string, unknown>, csrf: string) => { posted.push({ path, body, csrf }); return { ...answer, setCookies: [] }; }),
  csrfToken: vi.fn(async () => "csrf-token-0123456789"),
  readFlash: vi.fn(async () => null),
  field: (fd: FormData, k: string) => String(fd.get(k) ?? "").slice(0, 2000),
  formCsrf: (fd: FormData) => String(fd.get("csrf") ?? ""),
  setFlash: vi.fn(async () => undefined),
}));
vi.mock("next/navigation", () => ({ useRouter: () => ({ refresh() {}, replace() {} }), usePathname: () => null,
  redirect: (to: string) => { throw new Error("REDIRECT " + to); },
  notFound: () => { throw new Error("NOT FOUND"); },
}));

beforeEach(() => {
  for (const k of Object.keys(answers)) delete answers[k];
  asked.length = 0; posted.length = 0;
  answer = { status: 200, data: { ok: true, redirect: "/admin/fix/run?id=1" } };
});

async function html(el: React.ReactNode): Promise<string> {
  const { prelude } = await prerenderToNodeStream(el);
  const parts: Buffer[] = [];
  for await (const c of prelude) parts.push(Buffer.from(c as Uint8Array));
  return noInlineStyle(Buffer.concat(parts).toString("utf8").replace(/<!-- -->/g, ""));
}
const sp = (o: Record<string, string> = {}) => Promise.resolve(o);
const form = (o: Record<string, string | string[]>) => {
  const fd = new FormData();
  fd.set("csrf", "csrf-token-0123456789");
  for (const [k, v] of Object.entries(o)) for (const x of Array.isArray(v) ? v : [v]) fd.append(k, x);
  return fd;
};
const thrown = async (p: Promise<unknown>) => { try { await p; } catch (e) { return (e as Error).message; } return ""; };

describe("runs", () => {
  it("lists runs, the chart with its data, and passes the filters on", async () => {
    answers["/api/v1/runs"] = { status: 200, data: runs };
    const { default: Page } = await import("@/app/(app)/runs/page");
    const out = await html(await Page({ searchParams: sp({ task: "log-rca", other: "x" }) }));
    expect(asked[0]).toBe("/api/v1/runs?task=log-rca");
    expect(out).toContain("<h1>Agent runs</h1>");
    expect(out).toContain("/runs/view?id=" + runs.rows[0]!.id);
    expect(out).toContain("Show data");
    expect(out).toContain('class="bar-d"');
    expect(out).toContain('name="task"');
    expect(out).not.toMatch(/ style=|onclick/i);
  });
  it("says so when nothing matches or nothing ran", async () => {
    const { default: Page } = await import("@/app/(app)/runs/page");
    answers["/api/v1/runs"] = { status: 200, data: { ...runs, rows: [], list: { ...runs.list, filtered: true, matched: 0 } } };
    expect(await html(await Page({ searchParams: sp() }))).toContain("No run matches these filters");
    answers["/api/v1/runs"] = { status: 200, data: { ...runs, rows: [], days: runs.days.map((d) => ({ ...d, usd: 0 })), list: { ...runs.list, total: 0, matched: 0 } } };
    const out = await html(await Page({ searchParams: sp() }));
    expect(out).toContain("No runs yet");
    expect(out).toContain("No cost in the last 30 days");
    expect(out).not.toContain('role="search"');
  });
  it("shows one run: summary, messages with their chain, findings, record", async () => {
    answers["/api/v1/runs/item"] = { status: 200, data: agentRun };
    const { default: Page } = await import("@/app/(app)/runs/view/page");
    const out = await html(await Page({ searchParams: sp({ id: "1171" }) }));
    expect(out).toContain("chat-triage · run 1171");
    expect(out).toContain("Messages handled");
    expect(out).toContain("/incidents/problem?run=1171&amp;idx=0");
    expect(out).toContain("Findings");
    expect(out).toContain("/incidents/view?id=");
    expect(out).toContain("Record");
    answers["/api/v1/runs/item"] = { status: 404, data: {} };
    expect(await thrown(Page({ searchParams: sp({ id: "9" }) }))).toBe("NOT FOUND");
  });
});

describe("knowledge", () => {
  it("renders the lessons as elements, with the review link for an admin only", async () => {
    answers["/api/v1/knowledge"] = { status: 200, data: knowledge };
    const { default: Page } = await import("@/app/(app)/knowledge/page");
    let out = await html(await Page());
    expect(out).toContain("What Argus knows");
    expect(out).toContain("<h3");
    expect(out).toContain("/review?kind=lessons");
    answers["/api/v1/knowledge"] = { status: 200, data: { ...knowledge, admin: false } };
    out = await html(await Page());
    expect(out).not.toContain("/review?kind=lessons");
    answers["/api/v1/knowledge"] = { status: 200, data: { count: 0, md: "", services: [], admin: false } };
    expect(await html(await Page())).toContain("Nothing yet.");
  });
});

describe("fix runs", () => {
  it("lists runs for an admin and refuses a viewer", async () => {
    const { default: Page } = await import("@/app/(app)/admin/fix/page");
    answers["/api/v1/fix"] = { status: 200, data: fix };
    const out = await html(await Page());
    expect(out).toContain("Fixer set up");
    expect(out).toContain("/admin/fix/run?id=" + fix.rows[0]!.id);
    expect(out).toContain("Argus never fixes production");
    answers["/api/v1/fix"] = { status: 403, data: { error: "admins only" } };
    const no = await html(await Page());
    expect(no).toContain("Admins only.");
    expect(no).not.toContain("/admin/fix/run");
  });
  it("shows the report, and a run still waiting refreshes itself", async () => {
    const { default: Page } = await import("@/app/(app)/admin/fix/run/page");
    answers["/api/v1/fix/run"] = { status: 200, data: fixRun };
    const out = await html(await Page({ searchParams: sp({ id: fixRun.id }) }));
    for (const w of ["Rollback commands", "Actions", "Backups", "Verification", "Final report", "Exact prompt the fixer got"]) expect(out).toContain(w);
    expect(out).not.toContain('http-equiv="refresh"');
    answers["/api/v1/fix/run"] = { status: 200, data: { ...fixRun, report: null, refresh: true, wait: { text: "Queued: waiting.", detail: "", kind: "busy", steps: [] } } };
    const w = await html(await Page({ searchParams: sp({ id: fixRun.id }) }));
    expect(w).toContain("Queued: waiting.");
    expect(w).toMatch(/http-?equiv="refresh"/i);
    answers["/api/v1/fix/run"] = { status: 200, data: { ...fixRun, report: null, wait: { text: "Not started: not set up", detail: "", kind: "fail", steps: ["Run `setup.sh` first."] } } };
    const f = await html(await Page({ searchParams: sp({ id: fixRun.id }) }));
    expect(f).toContain("Operator steps");
    expect(f).toContain("<code>setup.sh</code>");
  });
  it("offers the review form with hosts, prompt and the tick; production only explains", async () => {
    const { default: Page } = await import("@/app/(app)/admin/fix/review/page");
    answers["/api/v1/fix/review"] = { status: 200, data: fixReview };
    const out = await html(await Page({ searchParams: sp({ run: "1187", idx: "0" }) }));
    expect(asked[0]).toBe("/api/v1/fix/review?run=1187&idx=0");
    expect(out).toContain(`value="${fixReview.hosts[0]}"`);
    expect(out).toContain('name="prompt"');
    expect(out).toContain('name="reviewed"');
    expect(out).toContain('name="csrf" value="csrf-token-0123456789"');
    expect(out).toContain('name="run" value="1187"');
    expect(out).toContain("Run fix");
    answers["/api/v1/fix/review"] = { status: 200, data: fixReviewProd };
    const p = await html(await Page({ searchParams: sp({ run: "1171", idx: "0" }) }));
    expect(p).toContain("never acts on production");
    expect(p).not.toContain('name="prompt"');
  });
});

describe("take action", () => {
  const fd = () => form({ run: "1187", idx: "0", prompt: "x".repeat(5000), host: ["a-fix", "b-fix"], reviewed: "1" });
  it("posts the whole prompt and every host, then follows the API", async () => {
    const { runFix } = await import("@/app/(app)/admin/fix/actions");
    expect(await thrown(runFix(null, fd()))).toBe("REDIRECT /admin/fix/run?id=1");
    expect(posted[0]!.path).toBe("/api/v1/fix/review");
    expect(posted[0]!.csrf).toBe("csrf-token-0123456789");
    expect(posted[0]!.body).toMatchObject({ run: "1187", idx: "0", host: ["a-fix", "b-fix"], reviewed: "1" });
    expect((posted[0]!.body.prompt as string).length).toBe(5000);
  });
  it("keeps the prompt and hosts on a refusal, in the demo and otherwise", async () => {
    const { runFix } = await import("@/app/(app)/admin/fix/actions");
    answer = { status: 403, data: { error: "Disabled in the demo" } };
    let s = await runFix(null, fd());
    expect(s).toMatchObject({ kind: "info", hosts: ["a-fix", "b-fix"] });
    expect(s.error).toContain("nothing changed");
    expect(s.prompt.length).toBe(5000);
    answer = { status: 400, data: { error: "Another fix is running; one at a time." } };
    s = await runFix(null, fd());
    expect(s).toMatchObject({ kind: "fail", error: "Another fix is running; one at a time." });
  });
  it("sends a stale session to the step-up and back", async () => {
    const { runFix } = await import("@/app/(app)/admin/fix/actions");
    answer = { status: 403, data: { error: "two-factor authentication required", mfa: "stepup", sensitive: true } };
    const to = await thrown(runFix(null, fd()));
    expect(to).toContain("REDIRECT /account/confirm");
    expect(to).toContain("sensitive=1");
    expect(decodeURIComponent(to)).toContain("/admin/fix/review?run=1187&idx=0");
  });
});

describe("review full lists", () => {
  it("ideas: tabs, filters, decisions and the advisers; a viewer gets none of the forms", async () => {
    const { default: ideas } = await import("./fixtures/ideas.json");
    answers["/api/v1/ideas"] = { status: 200, data: ideas };
    answers["/api/v1/review"] = { status: 500, data: {} };
    const { default: Page } = await import("@/app/(app)/review/page");
    const out = await html(await Page({ searchParams: sp({ kind: "ideas", mode: "full", status: "all" }) }));
    expect(asked[0]).toBe("/api/v1/ideas?kind=ideas&mode=full&status=all");
    for (const w of ["<h1>Ideas</h1>", "Advisers", "Generate now", 'name="persona"', "Mark under review", 'name="reason"', 'name="q"']) expect(out).toContain(w);
    expect(out).not.toMatch(/ style=|onclick/i);
    answers["/api/v1/ideas"] = { status: 200, data: { ...ideas, admin: false, rows: ideas.rows.map((r) => ({ ...r, actions: [] })), advisers: ideas.advisers.map((a) => ({ ...a, can: false })) } };
    const v = await html(await Page({ searchParams: sp({ kind: "ideas", mode: "full" }) }));
    expect(v).not.toContain(">Generate now<");
    expect(v).not.toContain("Mark under review");
  });
  it("proposals: rows with every control, repeated filters kept, settings; a viewer is refused", async () => {
    const { default: p } = await import("./fixtures/proposals.json");
    answers["/api/v1/proposals"] = { status: 200, data: p };
    const { default: Page } = await import("@/app/(app)/review/page");
    const out = await html(await Page({ searchParams: Promise.resolve({ kind: "proposals", mode: "full", sev: ["critical", "high"] }) }));
    expect(asked[0]).toBe("/api/v1/proposals?kind=proposals&sev=critical&sev=high&mode=full");
    for (const w of ["<h1>Proposed tasks</h1>", 'id="pbulk"', 'form="pbulk"', 'name="confirm"', 'name="title"', 'name="assignee"', 'name="expiry_days"', 'name="auto_qa"', "Snooze 24 h"]) expect(out).toContain(w);
    expect(out).toContain('value="/review?kind=proposals&amp;mode=full"');
    answers["/api/v1/proposals"] = { status: 403, data: { error: "admins only" } };
    const no = await html(await Page({ searchParams: sp({ kind: "proposals", mode: "full" }) }));
    expect(no).toContain("Admins only.");
    expect(no).not.toContain('name="title"');
  });
  it("proposals: the confirm step and the decided view", async () => {
    const { default: c } = await import("./fixtures/proposals-confirm.json");
    const { default: dd } = await import("./fixtures/proposals-decided.json");
    const { default: Page } = await import("@/app/(app)/review/page");
    answers["/api/v1/proposals"] = { status: 200, data: c };
    const out = await html(await Page({ searchParams: sp({ kind: "proposals", mode: "full", confirm: "reject" }) }));
    expect(out).toContain(c.confirm.head);
    expect(out).toContain(`name="id" value="${c.confirm.ids[0]}"`);
    expect(out).toContain('name="reason"');
    expect(out).toContain("Cancel");
    answers["/api/v1/proposals"] = { status: 200, data: dd };
    const d = await html(await Page({ searchParams: sp({ kind: "proposals", mode: "full", view: "decided" }) }));
    expect(d).toContain("Decided in the last 14 days");
    expect(d).toContain("Outcome");
  });
  it("writes post every field and come back to the full list", async () => {
    const { ideaAct, ideaGenerate, proposalAct, proposalSettings } = await import("@/app/(app)/review/actions");
    answer = { status: 200, data: { ok: true, redirect: "/review?kind=ideas&sent=accept&req=1#i-3" } };
    expect(await thrown(ideaAct(form({ id: "3", action: "accept", here: "/review?kind=ideas&mode=full" })))).toBe("REDIRECT /review?kind=ideas&mode=full&sent=accept&req=1#i-3");
    expect(posted[0]).toMatchObject({ path: "/api/v1/ideas/act", body: { id: "3", action: "accept" }, csrf: "csrf-token-0123456789" });
    answer = { status: 403, data: { error: "Disabled in the demo" } };
    expect(await thrown(ideaGenerate(form({ persona: "qa", here: "/review?kind=ideas&mode=full" })))).toBe("REDIRECT /review?kind=ideas&mode=full");
    answer = { status: 200, data: { ok: true, redirect: "/review?kind=proposals&mode=full&req=9" } };
    const back = "/review?kind=proposals&mode=full&view=snoozed";
    expect(await thrown(proposalAct(form({ id: ["1", "2"], action: "reject", reason: "Duplicate", return: back })))).toBe("REDIRECT /review?kind=proposals&mode=full&req=9");
    expect(posted[2]!.body).toMatchObject({ id: ["1", "2"], action: "reject", reason: "Duplicate", return: back });
    await thrown(proposalAct(form({ id: "1", action: "accept", edit: "1", title: "T", priority: "High", assignee: "", return: back })));
    expect(posted[3]!.body).toMatchObject({ edit: "1", title: "T", priority: "High", assignee: "" });
    await thrown(proposalSettings(form({ expiry_days: "7", reject_days: "5", auto_qa: "1", return: back })));
    expect(posted[4]).toMatchObject({ path: "/api/v1/proposals/settings", body: { expiry_days: "7", auto_qa: "1" } });
    answer = { status: 403, data: { error: "two-factor authentication required", mfa: "stepup", sensitive: true } };
    expect(await thrown(proposalSettings(form({ expiry_days: "7", reject_days: "5", return: back })))).toContain("REDIRECT /account/confirm");
  });
});

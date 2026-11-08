// Server-render tests of the Work section, from the API's real shapes (fixtures captured from the demo data as the admin,
// so the write controls are on), plus what a viewer sees and every refusal of a write.
import { prerenderToNodeStream } from "react-dom/static";
import { beforeEach, describe, expect, it, vi } from "vitest";
import boardLocal from "./fixtures/board-local.json";
import boardViewer from "./fixtures/board-viewer.json";
import askViewer from "./fixtures/ask-viewer.json";
import ask from "./fixtures/ask.json";
import board from "./fixtures/board.json";
import qaMessages from "./fixtures/qa-messages.json";
import qaViewer from "./fixtures/qa-viewer.json";
import qa from "./fixtures/qa.json";
import taskNew from "./fixtures/task-new.json";
import taskPlain from "./fixtures/task-plain.json";
import taskViewer from "./fixtures/task-viewer.json";
import task from "./fixtures/task.json";
import { noInlineStyle } from "./no-inline-style";

const answers: Record<string, { status: number; data: unknown }> = {};
const asked: string[] = [];
const posted: { path: string; body: Record<string, unknown>; csrf: string }[] = [];
let answer: { status: number; data: unknown } = { status: 200, data: { ok: true, redirect: "/board?req=1" } };
const flashes: string[] = [];

vi.mock("@/lib/server", () => ({
  back: async (to: string) => { throw new Error("REDIRECT " + to); },
  apiGet: vi.fn(async (path: string) => { asked.push(path); return { ...(answers[path.split("?")[0]!] ?? { status: 404, data: { error: "no fixture" } }), setCookies: [] }; }),
  apiPost: vi.fn(async (path: string, body: Record<string, unknown>, csrf: string) => { posted.push({ path, body, csrf }); return { ...answer, setCookies: [] }; }),
  csrfToken: vi.fn(async () => "csrf-token-0123456789"),
  readFlash: vi.fn(async () => null),
  field: (fd: FormData, k: string) => String(fd.get(k) ?? "").slice(0, 2000),
  formCsrf: (fd: FormData) => String(fd.get("csrf") ?? ""),
  setFlash: vi.fn(async (t: string) => { flashes.push(t); }),
}));
vi.mock("next/navigation", () => ({ useRouter: () => ({ refresh() {}, replace() {} }), usePathname: () => null,
  redirect: (to: string) => { throw new Error("REDIRECT " + to); },
  notFound: () => { throw new Error("NOT FOUND"); },
}));

beforeEach(() => {
  for (const k of Object.keys(answers)) delete answers[k];
  asked.length = 0; posted.length = 0; flashes.length = 0;
  answer = { status: 200, data: { ok: true, redirect: "/board?req=1" } };
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

describe("board", () => {
  it("folds a plan's write steps so the card stays short, with the warning still visible", async () => {
    answers["/api/v1/board"] = { status: 200, data: board };
    const { default: Page } = await import("@/app/(app)/board/page");
    const h = await html(await Page({ searchParams: sp({}) as never }));
    expect(h).toMatch(/<details class="kc-wf fold"><summary>.*?WRITE.*?step/);
    expect(h).toContain("prod-redis-01"); // folded, not dropped
  });

  it("renders the Jira mirror with its write controls", async () => {
    answers["/api/v1/board"] = { status: 200, data: board };
    const { default: Page } = await import("@/app/(app)/board/page");
    const h = await html(await Page({ searchParams: sp({ show: "all", qf: "1", evil: "x" }) as never }));
    expect(asked[0]).toBe("/api/v1/board?show=all&qf=1");
    expect(h).toContain("Refresh now");
    expect(h).toContain("New task");
    expect(h).toContain("Ask Argus");
    expect(h).toContain("OPS-232");
    expect(h).toContain("Only my issues");
    expect(h).toContain('name="col"');
    expect(h).toContain("csrf-token-0123456789");
    expect(h).toContain("Waiting for your OK");
    expect(h).not.toContain("style=");
    expect(h).not.toMatch(/\son[a-z]+=/);
  });
  it("shows a viewer no change control", async () => {
    answers["/api/v1/board"] = { status: 200, data: boardViewer };
    const { default: Page } = await import("@/app/(app)/board/page");
    const h = await html(await Page({ searchParams: sp() as never }));
    expect(h).not.toContain('name="col"');
    expect(h).not.toContain(">New task<");
    expect(h).not.toContain("Waiting for your OK");
  });
  it("renders Argus's own board with link filters", async () => {
    answers["/api/v1/board"] = { status: 200, data: boardLocal };
    const { default: Page } = await import("@/app/(app)/board/page");
    const h = await html(await Page({ searchParams: sp({ tracker: "all" }) as never }));
    expect(h).toContain("More filters");
    expect(h).toContain("Environment");
    expect(h).toContain('name="to"');
    expect(h).toContain("Move to…"); // never pre-selects a status: one click must not move a task
    expect(h).toMatch(/<select name="to"[^>]*required/);
  });
  it("follows a sent request with a reload and offers Jira's transitions", async () => {
    answers["/api/v1/board"] = { status: 200, data: { ...board, refresh: { seconds: 5 }, notices: [{ kind: "ok", text: "Sent." }], choice: { key: "OPS-1", col: "2", options: [{ value: "11", label: "Start → In Progress" }] } } };
    const { default: Page } = await import("@/app/(app)/board/page");
    const h = await html(await Page({ searchParams: sp() as never }));
    expect(h).toContain('http-equiv="refresh"');
    expect(h).toContain('name="transition"');
  });
});

describe("task page", () => {
  it("renders the agent panel, chat composer and comments for an admin", async () => {
    answers["/api/v1/board/task"] = { status: 200, data: task };
    const { default: Page } = await import("@/app/(app)/board/task/page");
    const h = await html(await Page({ searchParams: sp({ key: task.ref, junk: "1" }) as never }));
    expect(asked[0]).toBe("/api/v1/board/task?key=" + task.ref);
    expect(h).toContain("Argus works this task");
    expect(h).toContain("Chat with Argus");
    expect(h).toContain('name="text"');
    expect(h).toContain('name="body"');
    expect(h).toContain("Record");
    expect(h).not.toContain("style=");
  });
  it("shows a viewer the thread and no composer or spin up", async () => {
    answers["/api/v1/board/task"] = { status: 200, data: taskViewer };
    const { default: Page } = await import("@/app/(app)/board/task/page");
    const h = await html(await Page({ searchParams: sp({ key: task.ref }) as never }));
    expect(h).not.toContain('name="text"');
    expect(h).toContain("Viewers read the chat");
    expect(h).toContain("Admins spin up the agent");
  });
  it("disables the composer while Argus replies, and names production without alarm", async () => {
    const a = { ...task.agent, chat: { ...task.agent.chat, pending: true } };
    answers["/api/v1/board/task"] = { status: 200, data: { ...task, agent: a, prod: true, refresh: { seconds: 5 } } };
    const { default: Page } = await import("@/app/(app)/board/task/page");
    const h = await html(await Page({ searchParams: sp({ key: task.ref }) as never }));
    expect(h).toContain("Argus is replying");
    expect(h).toMatch(/<textarea[^>]*name="text"[^>]*disabled/);
    expect(h).toContain('content="5"');
    expect(h).toMatch(/class="banner info"[^]*Production: every step/);
  });
  it("renders progress, spin up, failure facts, result and the credentials forms", async () => {
    const a = { ...task.agent, finished: false, running: true, kind: "fail", spin: { can: true, label: "Spin up agent", reason: "", primary: true, proposal: false },
      replan: { can: true, why: "Plans again.", quiet: false }, facts: [{ label: "Exit code", value: "124" }],
      progress: { label: "Run progress", cur: 2, bar: true, now: [{ text: "reading" }], meta: [[{ text: "Stop refreshing", href: "/board/task?id=1&live=0#agent" }]], fix: false, steps: ["Queued", "Reading", "Working"] },
      result: { summary: "Done it", evidence: ["e1"], rollback: [], needs: [], moves: [{ col: "5", label: "Move to Done", primary: true }] },
      secrets: { items: [{ name: "API_KEY", text: "wiped", warn: "Revoke it" }], forms: [{ name: "DB_TOKEN", hosts: "cache-01", purpose: "", ttl: 240, label: "Provide DB_TOKEN" }], new: { name: "", hosts: "", purpose: "", ttl: 240, label: "Provide secret" } } };
    answers["/api/v1/board/task"] = { status: 200, data: { ...task, agent: a } };
    const { default: Page } = await import("@/app/(app)/board/task/page");
    const h = await html(await Page({ searchParams: sp({ key: task.ref }) as never }));
    for (const w of ["Plan again", "What failed exactly", "Stop refreshing", 'id="agent-stop"', 'name="plan_version"', "Move to Done", "Provide DB_TOKEN", 'type="password"', 'aria-current="step"']) expect(h).toContain(w);
  });
  it("renders the fixer form on a task Argus does not work", async () => {
    answers["/api/v1/board/task"] = { status: 200, data: { ...taskPlain, fixer: { heading: "Send to the fixer", notes: [], envLabel: "develop", hosts: [{ alias: "web-1", checked: true }] } } };
    const { default: Page } = await import("@/app/(app)/board/task/page");
    const h = await html(await Page({ searchParams: sp({ id: "1" }) as never }));
    expect(h).toContain('name="host"');
    expect(h).toContain('name="reviewed"');
  });
  it("says in words why a column is coloured, and gives every card its full values", async () => {
    const cols = board.columns.map((c, i) => (i === 0 ? { ...c, over: true, limits: "Max 1" } : c));
    answers["/api/v1/board"] = { status: 200, data: { ...board, columns: cols } };
    const { default: Page } = await import("@/app/(app)/board/page");
    const h = await html(await Page({ searchParams: sp() as never }));
    expect(h).toContain("Over limit · Max 1");
    expect(h).toContain('title="Jamie Poe"');
    expect(h).toContain('class="tabs"');
    expect(h).not.toContain('aria-current="location"');
  });
  it("is a 404 for an unknown task", async () => {
    const { default: Page } = await import("@/app/(app)/board/task/page");
    expect(await thrown(Page({ searchParams: sp({ id: "9" }) as never }))).toBe("NOT FOUND");
  });
});

describe("new task", () => {
  it("renders the form with the token and the limits", async () => {
    answers["/api/v1/board/new"] = { status: 200, data: taskNew };
    const { default: Page } = await import("@/app/(app)/board/new/page");
    const h = await html(await Page({ searchParams: sp() as never }));
    for (const w of ['name="title"', 'name="evidence"', 'name="fix_prompt"', "csrf-token-0123456789", "Create task"]) expect(h).toContain(w);
  });
  it("links the task that already exists", async () => {
    answers["/api/v1/board/new"] = { status: 200, data: { exists: { text: "OPS-1", href: "/board/task?id=1" } } };
    const { default: Page } = await import("@/app/(app)/board/new/page");
    const h = await html(await Page({ searchParams: sp({ src: "rca", id: "1" }) as never }));
    expect(h).toContain('href="/board/task?id=1"');
    expect(h).not.toContain('name="title"');
  });
  it("keeps the source through a step-up, and the selects keyed to their values", async () => {
    const { createTask } = await import("@/app/(app)/board/actions");
    answer = { status: 403, data: { mfa: "stepup", sensitive: false } };
    const to = await thrown(createTask(null, form({ title: "T", src: "rca", id: "52-1", return: "/board?show=all" })));
    expect(decodeURIComponent(to)).toContain("/board/new?src=rca&id=52-1&return=");
    expect(posted[0]!.body).toMatchObject({ src: "rca", id: "52-1" });
    const src = (await import("node:fs")).readFileSync("components/NewTaskForm.tsx", "utf8");
    for (const k of ["gl_project", "env", "priority"]) expect(src).toContain(`<select key={v.${k}} name="${k}"`);
  });
  it("keeps a long text whole and returns it on a refusal", async () => {
    const { createTask } = await import("@/app/(app)/board/actions");
    const long = "x".repeat(5000);
    answer = { status: 400, data: { error: "the title must be 1 to 200 characters." } };
    const s = await createTask(null, form({ title: " ", evidence: long, priority: "Medium" }));
    expect((posted[0]!.body as { evidence: string }).evidence.length).toBe(5000);
    expect(s).toMatchObject({ kind: "fail", error: "The title must be 1 to 200 characters." });
    expect(s.values.evidence?.length).toBe(5000);
  });
  it("says the demo refused, and goes to the board on success", async () => {
    const { createTask } = await import("@/app/(app)/board/actions");
    answer = { status: 403, data: { error: "Disabled in the demo" } };
    expect(await createTask(null, form({ title: "T" }))).toMatchObject({ kind: "info" });
    answer = { status: 200, data: { ok: true, redirect: "/board?req=9" } };
    expect(await thrown(createTask(null, form({ title: "T" })))).toBe("REDIRECT /board?req=9");
  });
});

describe("writes", () => {
  it("post to the right endpoint with the form's token and fields", async () => {
    const a = await import("@/app/(app)/board/actions");
    const cases: [(fd: FormData) => Promise<void>, Record<string, string | string[]>, string, Record<string, unknown>][] = [
      [a.boardMove, { task: "3", to: "done", back: "board", return: "/board" }, "/api/v1/board/move", { task: "3", to: "done", back: "board" }],
      [a.boardMove, { key: "OPS-1", col: "2", transition: "11", return: "/board" }, "/api/v1/board/jmove", { key: "OPS-1", col: "2", transition: "11" }],
      [a.boardComment, { task: "3", body: "c".repeat(3000) }, "/api/v1/board/comment", { task: "3", body: "c".repeat(3000) }],
      [a.boardAssign, { task: "3" }, "/api/v1/board/assign", { task: "3" }],
      [a.boardFixer, { task: "3", host: ["a", "b"], reviewed: "1" }, "/api/v1/board/fixer", { task: "3", host: ["a", "b"], reviewed: "1" }],
      [a.boardRefresh, {}, "/api/v1/board/refresh", {}],
      [a.matrixPreview, { pid: "p1", action: "create", assignee: "sam" }, "/api/v1/board/mxpreview", { pid: "p1", action: "create", assignee: "sam" }],
      [a.agentAct, { key: "OPS-1", action: "answers", instructions: "i", a_q1: "yes", other: "no" }, "/api/v1/board/agent", { key: "OPS-1", action: "answers", instructions: "i", a_q1: "yes" }],
      [a.agentSpin, { key: "OPS-1", plan_version: "2" }, "/api/v1/board/agent/spinup", { key: "OPS-1", plan_version: "2" }],
      [a.agentChat, { key: "OPS-1", text: "t".repeat(3000) }, "/api/v1/board/agent/chat", { key: "OPS-1", text: "t".repeat(3000) }],
    ];
    for (const [fn, f, path, body] of cases) {
      posted.length = 0;
      expect(await thrown(fn(form(f)))).toBe("REDIRECT /board?req=1");
      expect(posted[0]!.path).toBe(path);
      expect(posted[0]!.csrf).toBe("csrf-token-0123456789");
      expect(posted[0]!.body).toMatchObject(body);
      expect(posted[0]!.body).not.toHaveProperty("other");
    }
  });
  it("never lets a credential reach a flash or a URL", async () => {
    const { taskSecret } = await import("@/app/(app)/board/actions");
    const value = "v".repeat(4000);
    answer = { status: 400, data: { error: "the value is one line of 8 to 8192 bytes" } };
    const to = await thrown(taskSecret(form({ key: "OPS-1", name: "API_KEY", value, hosts: "h", ttl_min: "10", return: "/board/task?key=OPS-1" })));
    expect((posted[0]!.body as { value: string }).value.length).toBe(4000);
    expect(to).toBe("REDIRECT /board/task?key=OPS-1");
    expect(flashes.join()).not.toContain("vvvv");
  });
  it("sends a stale session to the step-up page and back", async () => {
    const { agentSpin } = await import("@/app/(app)/board/actions");
    answer = { status: 403, data: { error: "two-factor authentication required", mfa: "stepup", sensitive: true } };
    expect(await thrown(agentSpin(form({ key: "OPS-1", plan_version: "2", return: "/board/task?key=OPS-1" })))).toBe("REDIRECT /account/confirm?next=%2Fboard%2Ftask%3Fkey%3DOPS-1&sensitive=1");
  });
  it("tells the demo refusal in words and refuses another host as a return", async () => {
    const { boardAssign } = await import("@/app/(app)/board/actions");
    answer = { status: 403, data: { error: "Disabled in the demo" } };
    expect(await thrown(boardAssign(form({ task: "3", return: "https://elsewhere.example/" })))).toBe("REDIRECT /board");
    expect(flashes[0]).toContain("nothing was sent");
    answer = { status: 200, data: { ok: true, redirect: "//elsewhere.example/" } };
    expect(await thrown(boardAssign(form({ task: "3" })))).toBe("REDIRECT /board");
  });
});

describe("conversations and Ask Argus", () => {
  it("lists issues with the bulk link, filters and chain for an admin", async () => {
    answers["/api/v1/qa"] = { status: 200, data: qa };
    const { default: Page } = await import("@/app/(app)/qa/page");
    const h = await html(await Page({ searchParams: sp({ state: "all", junk: "1" }) as never }));
    expect(asked[0]).toBe("/api/v1/qa?state=all");
    for (const w of ["<h1>Conversations</h1>", 'id="pblink"', 'name="ref"', "qa_issue:", "Open issues", 'id="issue-', "Fix status"]) expect(h).toContain(w);
    const ids = [...h.matchAll(/\sid="([^"]+)"/g)].map((m) => m[1]);
    expect(new Set(ids).size).toBe(ids.length);
    expect(h).not.toContain("style=");
    expect(h).not.toMatch(/\son[a-z]+="/);
  });
  it("offers a viewer no bulk link", async () => {
    answers["/api/v1/qa"] = { status: 200, data: qaViewer };
    const { default: Page } = await import("@/app/(app)/qa/page");
    const h = await html(await Page({ searchParams: sp({ state: "all" }) as never }));
    expect(h).not.toContain("pblink");
  });
  it("lists every message with its dates form", async () => {
    answers["/api/v1/qa"] = { status: 200, data: qaMessages };
    const { default: Page } = await import("@/app/(app)/qa/page");
    const h = await html(await Page({ searchParams: sp({ view: "messages" }) as never }));
    for (const w of ["Apply", "Actions taken", 'name="view"']) expect(h).toContain(w);
  });
  it("folds a long message with a closable summary, and keeps the dates in the one filter form", async () => {
    const rows = qaMessages.rows.map((m, i) => (i === 0 ? { ...m, long: true, text: "A long report" } : m));
    answers["/api/v1/qa"] = { status: 200, data: { ...qaMessages, rows } };
    const { default: Page } = await import("@/app/(app)/qa/page");
    const h = await html(await Page({ searchParams: sp({ view: "messages" }) as never }));
    expect(h).toMatch(/<details class="qm"><summary><span class="qm-t"[^>]*>A long report<\/span><\/summary><p/);
    expect(h.match(/<form class="filters"/g)?.length).toBe(1);
    expect(h).toContain('name="from"');
    expect(h).toContain('class="l-table q-t"');
    expect(h).toContain('data-label="Disposition"');
  });
  it("says so when no triage has run", async () => {
    answers["/api/v1/qa"] = { status: 200, data: { view: "issues", ready: false, tabs: [], status: [], empty: "Issues appear after the next run." } };
    const { default: Page } = await import("@/app/(app)/qa/page");
    expect(await html(await Page({ searchParams: sp() as never }))).toContain("Issues appear after the next run.");
  });
  it("gives an admin the composer that starts a thread, and a viewer none", async () => {
    answers["/api/v1/board/ask"] = { status: 200, data: ask };
    const { default: Page } = await import("@/app/(app)/board/ask/page");
    const h = await html(await Page({ searchParams: sp() as never }));
    expect(h).toContain('name="text"');
    expect(h).toContain('value="new"');
    expect(h).toContain("Recent threads");
    answers["/api/v1/board/ask"] = { status: 200, data: askViewer };
    const v = await html(await Page({ searchParams: sp() as never }));
    expect(v).not.toContain('name="text"');
    expect(v).toContain("Viewers read the chat");
  });
  it("says when the chat is off, and is a 404 for a bad thread", async () => {
    answers["/api/v1/board/ask"] = { status: 200, data: { on: false, return: "/board/ask", chat: null, thread: false, threads: [], notices: [], refresh: null } };
    const { default: Page } = await import("@/app/(app)/board/ask/page");
    expect(await html(await Page({ searchParams: sp() as never }))).toContain("The task chat is off");
    delete answers["/api/v1/board/ask"];
    expect(await thrown(Page({ searchParams: sp({ key: "bad" }) as never }))).toBe("NOT FOUND");
  });
});

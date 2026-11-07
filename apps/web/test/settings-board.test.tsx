// Settings → Jira sync from the API's real shape (captured as the admin), then its writes through the Settings actions.
import { prerenderToNodeStream } from "react-dom/static";
import { beforeEach, describe, expect, it, vi } from "vitest";
import board from "./fixtures/admin-board.json";
import { noInlineStyle } from "./no-inline-style";

const answers: Record<string, { status: number; data: unknown }> = {};
const posted: { path: string; body: Record<string, unknown>; csrf: string }[] = [];
const flashes: string[] = [];
let answer: { status: number; data: unknown } = { status: 200, data: { ok: true, redirect: "/admin/board?req=1" } };

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
  answer = { status: 200, data: { ok: true, redirect: "/admin/board?req=1" } };
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
  answers["/api/v1/admin/board"] = { status: 200, data: d };
  const { default: Page } = await import("@/app/(app)/admin/board/page");
  return html(await Page({ searchParams: Promise.resolve(q) }));
};

const FAKE = "not-a-real-value"; // stands for a pasted credential
const shared = { facts: [{ label: "Connected as", value: "Argus bot" }], url: "https://jira.northwind.example", project: "OPS", boardId: "12", mode: "rw", approvers: "sam.roe", component: "api",
  projects: [{ value: "OPS", label: "OPS · Operations" }], boards: [{ value: "12", label: "12 · OPS board" }], components: ["api", "web"] };

describe("Jira sync", () => {
  it("shows the state, the connection, every section with its controls", async () => {
    const out = await render(board);
    for (const w of ["<h1>Jira sync</h1>", 'class="help-q"', "/help/settings#jira", "Sync now", "Full reconcile", "Connect Jira", "Personal access token", 'name="ca_pem"', "Propose a task", "Failed or dropped", "Retrying", "Task agent and QA intake", 'name="qa_cap"', 'name="fixer_trigger"', "Retire the fixer column", "Allowed Jira hosts", 'name="hosts"', "Connections", "How it syncs", "Connection", "Last good sync"])
      expect(out).toContain(w);
  });
  it("offers the settings, replace and disconnect once connected, and never a token value", async () => {
    const out = await render({ ...board, shared, failed: [{ at: 1, kind: "move", key: "OPS-1", taskId: 1, error: "conflict" }], agent: { ...board.agent, fixerTasks: [{ id: 3, key: "OPS-3", tracker: "jira", env: "develop", fixState: "-", title: "Old task" }] } });
    for (const w of ["Shared Jira connection", "Project for new tasks", 'name="board_pick"', 'name="component"', "Replace the token, URL or CA", "Disconnect shared Jira", 'name="action" value="shared-disconnect"', "Move these 1 to In Progress", 'name="expect" value="1"', "/board/task?id=1", "OPS-3"])
      expect(out).toContain(w);
    expect(out.match(/<input[^>]*name="token"[^>]*>/)?.[0]).not.toContain("value=");
  });
  it("is for admins", async () => {
    answers["/api/v1/admin/board"] = { status: 403, data: { error: "Admins only" } };
    const { default: Page } = await import("@/app/(app)/admin/board/page");
    const out = await html(await Page({ searchParams: Promise.resolve({}) }));
    expect(out).toContain("Admins only");
    expect(out).not.toContain("<form");
  });
  it("forwards the request id and refreshes while it waits", async () => {
    const out = await render({ ...board, notices: [{ text: "Sent.", kind: "ok" }], refresh: { seconds: 5 } }, { req: "20261104-ab" });
    expect(out).toContain("Sent.");
    expect(out).toContain('http-equiv="refresh"');
  });
});

describe("Settings actions", () => {
  it("save, then land on the page the API names", async () => {
    const { settingsAct } = await import("@/app/(app)/admin/settings");
    await expect(settingsAct(null, form({ _to: "agent", qa_cap: "3" }))).rejects.toThrow("REDIRECT /admin/board?req=1");
    expect(posted[0]).toEqual({ path: "/api/v1/admin/agent", body: { qa_cap: "3" }, csrf: "csrf-token-0123456789" });
  });
  it("take only a known route", async () => {
    const { settingsAct, settingsGo } = await import("@/app/(app)/admin/settings");
    expect(await settingsAct(null, form({ _to: "/api/v1/admin/users/act" }))).toMatchObject({ kind: "fail" });
    await expect(settingsGo(form({ _to: "nope" }))).rejects.toThrow("REDIRECT /admin");
    expect(posted).toHaveLength(0);
  });
  it("keep the entries on a refusal, but never a token", async () => {
    const { settingsAct } = await import("@/app/(app)/admin/settings");
    answer = { status: 400, data: { error: "Bad URL.", field: "url", form: "jira-connect" } };
    const st = await settingsAct(null, form({ _to: "jira-shared", url: "http://x", token: FAKE }));
    expect(st).toEqual({ error: "Not saved: Bad URL.", kind: "fail", field: "url", values: { url: "http://x" } });
    expect(JSON.stringify(st)).not.toContain(FAKE);
  });
  it("keep a switch whose name only sounds like a secret", async () => {
    const { settingsAct } = await import("@/app/(app)/admin/settings");
    answer = { status: 400, data: { error: "Too many.", field: "qa_cap" } };
    expect((await settingsAct(null, form({ _to: "agent", secrets: "1", qa_cap: "99" }))).values).toEqual({ secrets: "1", qa_cap: "99" });
  });
  it("say so in the demo and send a stale code to the step-up page", async () => {
    const { settingsAct, settingsGo } = await import("@/app/(app)/admin/settings");
    answer = { status: 403, data: { error: "Disabled in the demo" } };
    expect(await settingsAct(null, form({ _to: "jira-hosts", hosts: "a.example" }))).toMatchObject({ kind: "info", values: { hosts: "a.example" } });
    await expect(settingsGo(form({ _to: "board", action: "sync" }))).rejects.toThrow("REDIRECT /admin/board");
    expect(flashes[0]).toContain("Disabled in the demo");
    answer = { status: 403, data: { error: "two-factor authentication required", mfa: "stepup", sensitive: true } };
    await expect(settingsAct(null, form({ _to: "agent" }))).rejects.toThrow(/REDIRECT \/account\/confirm\?next=%2Fadmin%2Fboard%23agent&sensitive=1/);
    await expect(settingsGo(form({ _to: "board" }))).rejects.toThrow(/REDIRECT \/account\/confirm/);
  });
});

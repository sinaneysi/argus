// Server-render tests of Review's full lists of lessons, problem links and chat requests, from the API's real shapes
// (captured as the admin, so the write controls are on), then the writes through their server actions.
import { prerenderToNodeStream } from "react-dom/static";
import { beforeEach, describe, expect, it, vi } from "vitest";
import lessonsApproved from "./fixtures/lessons-approved.json";
import lessons from "./fixtures/lessons.json";
import replies from "./fixtures/replies.json";
import suggestionsConfirm from "./fixtures/suggestions-confirm.json";
import suggestionsViewer from "./fixtures/suggestions-viewer.json";
import suggestions from "./fixtures/suggestions.json";
import { noInlineStyle } from "./no-inline-style";

const answers: Record<string, { status: number; data: unknown }> = {};
const asked: string[] = [];
const posted: { path: string; body: Record<string, unknown>; csrf: string }[] = [];
const flashes: string[] = [];
let answer: { status: number; data: unknown } = { status: 200, data: { ok: true, redirect: "/review?kind=lessons&req=1" } };

vi.mock("@/lib/server", () => ({
  apiGet: vi.fn(async (path: string) => { asked.push(path); return { ...(answers[path.split("?")[0]!] ?? { status: 404, data: { error: "no" } }), setCookies: [] }; }),
  apiPost: vi.fn(async (path: string, body: Record<string, unknown>, csrf: string) => { posted.push({ path, body, csrf }); return { ...answer, setCookies: [] }; }),
  csrfToken: vi.fn(async () => "csrf-token-0123456789"),
  readFlash: vi.fn(async () => null),
  field: (fd: FormData, k: string) => String(fd.get(k) ?? "").slice(0, 2000),
  formCsrf: (fd: FormData) => String(fd.get("csrf") ?? ""),
  setFlash: vi.fn(async (m: string) => { flashes.push(m); }),
}));
vi.mock("next/navigation", () => ({
  redirect: (to: string) => { throw new Error("REDIRECT " + to); },
  notFound: () => { throw new Error("NOT FOUND"); },
}));

beforeEach(() => {
  for (const k of Object.keys(answers)) delete answers[k];
  asked.length = 0; posted.length = 0; flashes.length = 0;
  answer = { status: 200, data: { ok: true, redirect: "/review?kind=lessons&req=1" } };
});

async function html(el: React.ReactNode): Promise<string> {
  const { prelude } = await prerenderToNodeStream(el);
  const parts: Buffer[] = [];
  for await (const c of prelude) parts.push(Buffer.from(c as Uint8Array));
  return noInlineStyle(Buffer.concat(parts).toString("utf8").replace(/<!-- -->/g, ""));
}
const form = (o: Record<string, string | string[]>) => {
  const fd = new FormData();
  fd.set("csrf", "csrf-token-0123456789");
  for (const [k, v] of Object.entries(o)) for (const x of Array.isArray(v) ? v : [v]) fd.append(k, x);
  return fd;
};
const LESSONS = "/review?kind=lessons&mode=full";

describe("lessons", () => {
  it("shows the states, the filters, the decisions, Edit and approve and the switch", async () => {
    answers["/api/v1/lessons"] = { status: 200, data: lessons };
    const { LessonsFull } = await import("@/app/(app)/review/Lessons");
    const out = await html(await LessonsFull({ params: { kind: "lessons", mode: "full" } }));
    expect(out).toContain("<h1>Lessons</h1>");
    for (const w of ["Waiting", "Approved", "Rejected", "Retired", ">Approve<", ">Reject<", "Edit and approve", "Save and approve", 'name="body"', 'name="enabled"', "Save setting", "What Argus knows"]) expect(out).toContain(w);
    expect(out).toContain(`value="${LESSONS.replace("&", "&amp;")}"`);
    expect(out).toContain('name="lkind"');
    expect(out).not.toContain(">None<");
  });
  it("offers Back to review and Retire on an approved lesson", async () => {
    answers["/api/v1/lessons"] = { status: 200, data: lessonsApproved };
    const { LessonsFull } = await import("@/app/(app)/review/Lessons");
    const out = await html(await LessonsFull({ params: { kind: "lessons", mode: "full", view: "approved" } }));
    expect(out).toContain("Back to review");
    expect(out).toContain(">Retire<");
    expect(out).toContain("used ");
  });
  it("is for admins", async () => {
    answers["/api/v1/lessons"] = { status: 403, data: { error: "Admins only" } };
    const { LessonsFull } = await import("@/app/(app)/review/Lessons");
    const out = await html(await LessonsFull({ params: {} }));
    expect(out).toContain("Admins only");
    expect(out).not.toContain("<form");
  });
  it("posts a decision and lands back on the full list", async () => {
    const { lessonAct } = await import("@/app/(app)/review/actions");
    await expect(lessonAct(form({ id: "5", action: "approve", return: LESSONS }))).rejects.toThrow("REDIRECT /review?kind=lessons&mode=full&req=1");
    expect(posted[0]).toEqual({ path: "/api/v1/lessons/act", body: { id: "5", action: "approve", return: LESSONS }, csrf: "csrf-token-0123456789" });
  });
  it("keeps what was typed on a refused edit and in the demo", async () => {
    const { lessonEdit } = await import("@/app/(app)/review/actions");
    answer = { status: 400, data: { error: "Not saved: Title must be 1 to 120 characters." } };
    const typed = { id: "5", title: "", body: "kept", service: "a/b", env: "", kind: "", tags: "x", return: LESSONS };
    expect(await lessonEdit(null, form(typed))).toMatchObject({ error: "Not saved: Title must be 1 to 120 characters.", kind: "fail", values: { body: "kept", service: "a/b" } });
    answer = { status: 403, data: { error: "Disabled in the demo" } };
    expect(await lessonEdit(null, form(typed))).toMatchObject({ kind: "info", values: { body: "kept" } });
    expect(posted[0]!.body).toMatchObject({ action: "edit", id: "5" });
  });
  it("goes to the step-up page when a recent code is needed", async () => {
    const { lessonSettings } = await import("@/app/(app)/review/actions");
    answer = { status: 403, data: { error: "two-factor authentication required", mfa: "stepup", sensitive: true } };
    await expect(lessonSettings(form({ enabled: "1", return: LESSONS }))).rejects.toThrow(/REDIRECT \/account\/confirm/);
  });
});

describe("problem links", () => {
  it("lists the suggestions with one-by-one decisions and the bulk bar", async () => {
    answers["/api/v1/suggestions"] = { status: 200, data: suggestions };
    const { LinksFull } = await import("@/app/(app)/review/Links");
    const out = await html(await LinksFull({ query: "kind=links&mode=full" }));
    for (const w of ["<h1>Suggestions</h1>", "Accept link", 'form="pbsug"', 'id="pbsug"', "Accept links…", 'name="conf"', "Select all", 'name="note"']) expect(out).toContain(w);
    expect(asked[0]).toBe("/api/v1/suggestions?kind=links&mode=full");
  });
  it("gives a viewer no control", async () => {
    answers["/api/v1/suggestions"] = { status: 200, data: suggestionsViewer };
    const { LinksFull } = await import("@/app/(app)/review/Links");
    const out = await html(await LinksFull({ query: "" }));
    for (const w of ["Accept link", 'form="pbsug"', 'name="note"', "Select all"]) expect(out).not.toContain(w);
  });
  it("shows the confirm step with one POST", async () => {
    answers["/api/v1/suggestions"] = { status: 200, data: suggestionsConfirm };
    const { LinksFull } = await import("@/app/(app)/review/Links");
    const out = await html(await LinksFull({ query: "confirm=reject" }));
    expect(out).toContain("Reject 1 suggested link?");
    expect(out).toContain('name="link"');
    expect(out).toContain("Cancel");
    expect(out).not.toContain('id="pbsug"');
  });
  it("says why a confirm step cannot run", async () => {
    answers["/api/v1/suggestions"] = { status: 200, data: { ...suggestionsConfirm, confirm: { error: "Choose 1 to 50 suggestions, then an action.", note: "Nothing was changed.", back: "/review" } } };
    const { LinksFull } = await import("@/app/(app)/review/Links");
    expect(await html(await LinksFull({ query: "confirm=accept" }))).toContain("Choose 1 to 50 suggestions");
  });
});

describe("chat requests", () => {
  it("lets the admin choose the assignee", async () => {
    answers["/api/v1/replies"] = { status: 200, data: replies };
    const { RepliesFull } = await import("@/app/(app)/review/Replies");
    const out = await html(await RepliesFull({ params: {} }));
    for (const w of ["<h1>Chat requests</h1>", 'name="assignee"', "Unassigned", 'value="create"', 'value="discard"']) expect(out).toContain(w);
  });
  it("posts through the board's twin and comes back", async () => {
    const { replyAct } = await import("@/app/(app)/review/actions");
    answer = { status: 200, data: { ok: true, redirect: "/review?kind=replies&mode=full&req=2" } };
    await expect(replyAct(form({ pid: "p1", action: "create", assignee: "a.b", return: "/review?kind=replies&mode=full" }))).rejects.toThrow("REDIRECT /review?kind=replies&mode=full&req=2");
    expect(posted[0]!.path).toBe("/api/v1/board/mxpreview");
  });
  it("shows the demo refusal", async () => {
    const { replyAct } = await import("@/app/(app)/review/actions");
    answer = { status: 403, data: { error: "Disabled in the demo" } };
    await expect(replyAct(form({ pid: "p1", action: "discard", return: "/review?kind=replies&mode=full" }))).rejects.toThrow("REDIRECT");
    expect(flashes[0]).toContain("Disabled in the demo");
  });
});

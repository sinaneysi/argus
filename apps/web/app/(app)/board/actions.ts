"use server";

import { redirect } from "next/navigation";
import { errorOf } from "@/lib/api";
import { safeNext, stepUpPath } from "@/lib/format";
import { apiPost, field, formCsrf, setFlash } from "@/lib/server";

// The Work section's writes. Each posts to the API, which keeps the Go routes' roles, 2FA and request files.

type Answer = { ok?: boolean; redirect?: string };
type Body = Record<string, string | string[] | number | boolean | undefined>;

/** A long field whole: field() stops at 2000 characters, and the API checks every limit. */
const long = (fd: FormData, k: string): string => {
  const v = fd.get(k);
  return typeof v === "string" ? v : "";
};

const DEMO = "Disabled in the demo";
const DEMO_WORDS = "Disabled in the demo: nothing was sent and nothing changed.";

async function send(path: string, body: Body, fd: FormData, fallback = "/board"): Promise<void> {
  const here = safeNext(field(fd, "return"), fallback);
  const r = await apiPost<Answer>(path, { ...body, return: field(fd, "return") || undefined }, formCsrf(fd));
  if (r.status === 200 && r.data.ok) redirect(safeNext(r.data.redirect, here));
  const up = stepUpPath(r.data, here);
  if (up) redirect(up);
  const msg = errorOf(r.data, "Not sent.");
  await setFlash(msg === DEMO ? DEMO_WORDS : msg, msg === DEMO ? "info" : "fail");
  redirect(here);
}

/** Move a card: Argus's column, or a column of the Jira board (with the chosen transition when Jira offers several). */
export async function boardMove(fd: FormData): Promise<void> {
  if (fd.has("key")) {
    await send("/api/v1/board/jmove", { key: field(fd, "key"), col: field(fd, "col"), transition: field(fd, "transition") || undefined, back: field(fd, "back") || undefined }, fd);
    return;
  }
  await send("/api/v1/board/move", { task: field(fd, "task"), to: field(fd, "to"), back: field(fd, "back") || undefined }, fd);
}

export async function boardRefresh(fd: FormData): Promise<void> {
  await send("/api/v1/board/refresh", {}, fd);
}

export async function boardComment(fd: FormData): Promise<void> {
  await send("/api/v1/board/comment", { task: field(fd, "task"), body: long(fd, "body") }, fd);
}

export async function boardAssign(fd: FormData): Promise<void> {
  await send("/api/v1/board/assign", { task: field(fd, "task") }, fd);
}

export async function boardFixer(fd: FormData): Promise<void> {
  const hosts = fd.getAll("host").filter((h): h is string => typeof h === "string");
  await send("/api/v1/board/fixer", { task: field(fd, "task"), host: hosts, reviewed: field(fd, "reviewed") }, fd);
}

/** A chat request of Matrix that waits for this admin: create it, or discard it. */
export async function matrixPreview(fd: FormData): Promise<void> {
  await send("/api/v1/board/mxpreview", { pid: field(fd, "pid"), action: field(fd, "action"), assignee: field(fd, "assignee") }, fd);
}

/** Direct the task agent: stop it, plan again, or (chat off) save answers and instructions. */
export async function agentAct(fd: FormData): Promise<void> {
  const body: Body = { key: field(fd, "key"), action: field(fd, "action") };
  if (fd.has("instructions")) body.instructions = long(fd, "instructions");
  if (fd.has("post_jira")) body.post_jira = field(fd, "post_jira");
  for (const k of fd.keys()) if (/^a_q[0-9]+$/.test(k)) body[k] = long(fd, k);
  await send("/api/v1/board/agent", body, fd);
}

/** An admin approves exactly the plan version on the page (recent 2FA code). */
export async function agentSpin(fd: FormData): Promise<void> {
  await send("/api/v1/board/agent/spinup", { key: field(fd, "key"), plan_version: field(fd, "plan_version") }, fd);
}

export async function agentChat(fd: FormData): Promise<void> {
  await send("/api/v1/board/agent/chat", { key: field(fd, "key"), text: long(fd, "text") }, fd);
}

/** An admin gives the task a credential. The value goes to the API and nowhere else: never a flash, a URL or a log. */
export async function taskSecret(fd: FormData): Promise<void> {
  await send("/api/v1/board/task/secret", { key: field(fd, "key"), name: field(fd, "name"), value: long(fd, "value"), hosts: field(fd, "hosts"), ttl_min: field(fd, "ttl_min"), purpose: field(fd, "purpose") }, fd);
}

export type CreateState = { error: string; kind: "fail" | "info"; values: Record<string, string> };

const CREATE_KEYS = ["title", "env", "host", "priority", "labels", "description", "evidence", "fix_prompt", "tracker", "gl_project"];

/** New task. A refusal comes back as state, so the form keeps what was typed (DESIGN.md "Settings and forms"). */
export async function createTask(_: CreateState | null, fd: FormData): Promise<CreateState> {
  const values: Record<string, string> = {};
  for (const k of CREATE_KEYS) values[k] = long(fd, k);
  const ret = field(fd, "return");
  const src = field(fd, "src");
  const id = field(fd, "id");
  const back = new URLSearchParams();
  if (src) { back.set("src", src); back.set("id", id); }
  if (ret) back.set("return", ret);
  const here = "/board/new" + (back.size ? "?" + back.toString() : "");
  const r = await apiPost<Answer>("/api/v1/board/create", { ...values, src: src || undefined, id: id || undefined, return: ret || undefined }, formCsrf(fd));
  if (r.status === 200 && r.data.ok) redirect(safeNext(r.data.redirect, "/board"));
  const up = stepUpPath(r.data, here);
  if (up) redirect(up);
  const msg = errorOf(r.data, "Not sent.");
  return { error: msg === DEMO ? DEMO_WORDS : msg, kind: msg === DEMO ? "info" : "fail", values };
}

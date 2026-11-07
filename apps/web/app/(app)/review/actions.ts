"use server";

import { redirect } from "next/navigation";
import { errorOf } from "@/lib/api";
import { safeNext, stepUpPath } from "@/lib/format";
import { apiPost, field, formCsrf, setFlash } from "@/lib/server";

/** A decision from the review panel: the API posts it to the kind's own handler (same fields, roles and 2FA class). */
export async function reviewAct(fd: FormData): Promise<void> {
  const ret = safeNext(field(fd, "return"), "/review");
  const here = safeNext(field(fd, "here"), ret);
  const r = await apiPost<{ ok?: boolean; redirect?: string }>("/api/v1/review/act", {
    sel: field(fd, "sel"), action: field(fd, "action"), reason: field(fd, "reason") || undefined,
    plan_version: field(fd, "plan_version") || undefined, return: ret,
  }, formCsrf(fd));
  if (r.status === 200 && r.data.ok) redirect(safeNext(r.data.redirect, ret));
  const up = stepUpPath(r.data, here);
  if (up) redirect(up);
  const msg = errorOf(r.data, "Not sent.");
  await setFlash(msg === "Disabled in the demo" ? "Disabled in the demo: nothing was sent and nothing changed." : msg,
    msg === "Disabled in the demo" ? "info" : "fail");
  redirect(here);
}

/** A write of a full list: back to that list with the API's banner; a refusal comes back as a flash. */
async function listWrite(path: string, body: Record<string, string | string[] | undefined>, fd: FormData, kind: string): Promise<void> {
  const here = safeNext(field(fd, "here"), `/review?kind=${kind}&mode=full`);
  const r = await apiPost<{ ok?: boolean; redirect?: string }>(path, body, formCsrf(fd));
  if (r.status === 200 && r.data.ok) {
    const to = safeNext(r.data.redirect, here);
    redirect(to.includes("mode=full") ? to : to.replace(`/review?kind=${kind}&`, `/review?kind=${kind}&mode=full&`));
  }
  const up = stepUpPath(r.data, here);
  if (up) redirect(up);
  const msg = errorOf(r.data, "Not sent.");
  await setFlash(msg === "Disabled in the demo" ? "Disabled in the demo: nothing was sent and nothing changed." : msg, msg === "Disabled in the demo" ? "info" : "fail");
  redirect(here);
}

export async function ideaAct(fd: FormData): Promise<void> {
  await listWrite("/api/v1/ideas/act", { id: field(fd, "id"), action: field(fd, "action"), reason: field(fd, "reason") || undefined, ref: field(fd, "ref") || undefined }, fd, "ideas");
}

export async function ideaGenerate(fd: FormData): Promise<void> {
  await listWrite("/api/v1/ideas/generate", { persona: field(fd, "persona") }, fd, "ideas");
}

/** One row or the bulk confirm step; a long reason or title is checked by the API. */
export async function proposalAct(fd: FormData): Promise<void> {
  const body: Record<string, string | string[] | undefined> = { id: fd.getAll("id").map(String), action: field(fd, "action"), return: field(fd, "return") };
  for (const k of ["reason", "edit", "tracker", "gl_project", "title", "assignee", "project", "priority", "labels"]) if (fd.has(k)) body[k] = field(fd, k);
  fd.set("here", field(fd, "return"));
  await listWrite("/api/v1/proposals/act", body, fd, "proposals");
}

export async function proposalSettings(fd: FormData): Promise<void> {
  const body: Record<string, string | string[] | undefined> = { expiry_days: field(fd, "expiry_days"), reject_days: field(fd, "reject_days"), return: field(fd, "return") };
  for (const k of fd.keys()) if (k.startsWith("auto_")) body[k] = field(fd, k);
  fd.set("here", field(fd, "return"));
  await listWrite("/api/v1/proposals/settings", body, fd, "proposals");
}

export type LessonEditState = { error: string; kind: "fail" | "info"; values: Record<string, string> };

export async function lessonAct(fd: FormData): Promise<void> {
  fd.set("here", field(fd, "return"));
  await listWrite("/api/v1/lessons/act", { id: field(fd, "id"), action: field(fd, "action"), return: field(fd, "return") }, fd, "lessons");
}

/** Edit and approve: a refusal comes back with what was typed; the API checks every field. */
export async function lessonEdit(_: LessonEditState | null, fd: FormData): Promise<LessonEditState> {
  const values: Record<string, string> = {};
  for (const k of ["title", "body", "service", "env", "kind", "tags"]) values[k] = field(fd, k);
  const here = safeNext(field(fd, "return"), "/review?kind=lessons&mode=full");
  const r = await apiPost<{ ok?: boolean; redirect?: string }>("/api/v1/lessons/act", { ...values, id: field(fd, "id"), action: "edit", return: here }, formCsrf(fd));
  if (r.status === 200 && r.data.ok) {
    const to = safeNext(r.data.redirect, here);
    redirect(to.includes("mode=full") ? to : to.replace("/review?kind=lessons&", "/review?kind=lessons&mode=full&"));
  }
  const up = stepUpPath(r.data, here);
  if (up) redirect(up);
  const msg = errorOf(r.data, "Not saved.");
  const demo = msg === "Disabled in the demo";
  return { error: demo ? "Disabled in the demo: nothing was sent and nothing changed." : msg, kind: demo ? "info" : "fail", values };
}

export async function lessonSettings(fd: FormData): Promise<void> {
  fd.set("here", field(fd, "return"));
  await listWrite("/api/v1/lessons/settings", { enabled: field(fd, "enabled"), return: field(fd, "return") }, fd, "lessons");
}

/** A chat request: create the task with the chosen assignee, or discard it. */
export async function replyAct(fd: FormData): Promise<void> {
  fd.set("here", field(fd, "return"));
  await listWrite("/api/v1/board/mxpreview", { pid: field(fd, "pid"), action: field(fd, "action"), assignee: field(fd, "assignee"), return: field(fd, "return") }, fd, "replies");
}

"use server";

import { redirect } from "next/navigation";
import { errorOf } from "@/lib/api";
import { safeNext, stepUpPath } from "@/lib/format";
import { apiPost, field, formCsrf, setFlash } from "@/lib/server";

// The Infrastructure section's writes. Each posts to the API, which keeps the old pages' roles, 2FA and request files.

type Answer = { ok?: boolean; redirect?: string; errors?: string[] };

async function finish(r: { status: number; data: Answer }, here: string): Promise<void> {
  if (r.status === 200 && r.data.ok) {
    if (r.data.errors?.length) await setFlash("Not yet: " + r.data.errors.join(" "), "fail");
    redirect(safeNext(r.data.redirect, here));
  }
  const up = stepUpPath(r.data, here);
  if (up) redirect(up);
  const msg = errorOf(r.data, "Not sent.");
  const demo = msg === "Disabled in the demo";
  await setFlash(demo ? "Disabled in the demo: nothing was sent and nothing changed." : msg, demo ? "info" : "fail");
  redirect(here);
}

/** Scan now: one request file per task; the page shows the progress. */
export async function scanAct(fd: FormData): Promise<void> {
  const back = safeNext(field(fd, "back"), "/operations");
  await finish(await apiPost<Answer>("/api/v1/scan", { tasks: field(fd, "tasks"), back }, formCsrf(fd)), back);
}

/** Refresh or Analyse one VM. */
export async function vmAct(fd: FormData): Promise<void> {
  const id = field(fd, "id");
  const what = field(fd, "what") === "analyse" ? "analyse" : "refresh";
  await finish(await apiPost<Answer>(`/api/v1/inventory/${what}`, { id }, formCsrf(fd)), `/inventory/view?id=${encodeURIComponent(id)}`);
}

export async function capacityAnalyse(fd: FormData): Promise<void> {
  await finish(await apiPost<Answer>("/api/v1/capacity/analyse", { fs: field(fd, "fs") }, formCsrf(fd)), "/capacity");
}

/** Mark a failing set as OK, or undo it (admins, a recent 2FA code). */
export async function backupOverride(fd: FormData): Promise<void> {
  const here = safeNext(field(fd, "back"), "/backups");
  const body: Record<string, string> = { back: here };
  for (const k of ["action", "id", "set", "check", "scope", "days", "reason"]) if (fd.has(k)) body[k] = field(fd, k);
  await finish(await apiPost<Answer>("/api/v1/backups/override", body, formCsrf(fd)), here);
}

export async function backupInstall(fd: FormData): Promise<void> {
  const plan = field(fd, "plan");
  await finish(await apiPost<Answer>("/api/v1/backups/install", { plan }, formCsrf(fd)), `/backups/plan?id=${encodeURIComponent(plan)}`);
}

export async function backupRestore(fd: FormData): Promise<void> {
  const plan = field(fd, "plan");
  const body: Record<string, string> = { plan };
  for (const k of ["date", "result", "object", "notes"]) body[k] = field(fd, k);
  await finish(await apiPost<Answer>("/api/v1/backups/restore", body, formCsrf(fd)), `/backups/plan?id=${encodeURIComponent(plan)}#restore`);
}

/** Test, Save or Preview. The connection string goes to the API in this body and nowhere else; it is never logged or echoed. */
export async function backupNew(fd: FormData): Promise<void> {
  const body: Record<string, string> = {};
  for (const k of ["action", "uri", "databases", "schedule", "cron", "target", "member", "name", "retention_days", "draft", "sha", "token"]) if (fd.has(k)) body[k] = field(fd, k);
  await finish(await apiPost<Answer>("/api/v1/backups/new", body, formCsrf(fd)), "/backups/new");
}

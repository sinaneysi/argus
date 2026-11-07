"use server";

import { redirect } from "next/navigation";
import { errorOf } from "@/lib/api";
import { safeNext, stepUpPath } from "@/lib/format";
import { apiPost, field, formCsrf, setFlash } from "@/lib/server";

type Answer = { ok?: boolean; redirect?: string };

/** A write's answer: follow it, or come back to the page with the refusal in words. */
async function finish(r: { status: number; data: Answer }, here: string): Promise<void> {
  if (r.status === 200 && r.data.ok) redirect(safeNext(r.data.redirect, here));
  const up = stepUpPath(r.data, here);
  if (up) redirect(up);
  const msg = errorOf(r.data, "Not sent.");
  const demo = msg === "Disabled in the demo";
  await setFlash(demo ? "Disabled in the demo: nothing was sent and nothing changed." : msg, demo ? "info" : "fail");
  redirect(here);
}

/** Acknowledge, resolve or re-open (the API writes the request file; roles as on the old page). */
export async function incidentAct(fd: FormData): Promise<void> {
  const id = field(fd, "id");
  const r = await apiPost<Answer>("/api/v1/incidents/act", { id, action: field(fd, "action"), note: field(fd, "note") || undefined }, formCsrf(fd));
  await finish(r, `/incidents/view?id=${encodeURIComponent(id)}`);
}

/** "Investigate deeply": a read-only deep RCA, admins only. */
export async function incidentDeep(fd: FormData): Promise<void> {
  const id = field(fd, "id");
  await finish(await apiPost<Answer>("/api/v1/incidents/deep", { id }, formCsrf(fd)), `/incidents/view?id=${encodeURIComponent(id)}#rca-reports`);
}

const PROBLEM_FIELDS = ["action", "problem", "pnum", "link_type", "note", "title", "from", "to", "root_cause", "root_cause_label", "workaround", "owner", "into"] as const;

/** Every Problem control: one request file, applied by Argus. A field the form does not carry stays out of the body. */
export async function problemAct(fd: FormData): Promise<void> {
  const here = safeNext(field(fd, "back"), "/problems");
  const body: Record<string, string | string[]> = { back: here };
  for (const k of PROBLEM_FIELDS) if (fd.has(k)) body[k] = field(fd, k);
  for (const k of ["ref", "link", "issue"]) {
    const v = fd.getAll(k).filter((x): x is string => typeof x === "string" && x !== "");
    if (v.length) body[k] = v;
  }
  await finish(await apiPost<Answer>("/api/v1/problems/act", body, formCsrf(fd)), here.split("?sent=")[0] ?? here);
}

"use server";

import { redirect } from "next/navigation";
import { errorOf } from "@/lib/api";
import { safeNext, stepUpPath } from "@/lib/format";
import { apiPost, field, formCsrf, setFlash } from "@/lib/server";

const FIELDS = ["action", "item", "reason", "minutes", "days", "focus_min", "max_items", "max_risky"] as const;

/** Every Today form: done, start, skip, defer, undo, today, replan, settings (the API writes the request file). */
export async function todayAct(fd: FormData): Promise<void> {
  const body: Record<string, string> = {};
  for (const k of FIELDS) {
    const v = field(fd, k);
    if (v !== "") body[k] = v;
  }
  const r = await apiPost<{ ok?: boolean; redirect?: string }>("/api/v1/today/act", body, formCsrf(fd));
  if (r.status === 200 && r.data.redirect) redirect(safeNext(r.data.redirect, "/today"));
  const up = stepUpPath(r.data, "/today");
  if (up) redirect(up);
  await setFlash(errorOf(r.data, "Not sent."), r.status === 403 && errorOf(r.data) === "Disabled in the demo" ? "info" : "fail");
  redirect("/today" + (body.item ? "#t-" + body.item : ""));
}

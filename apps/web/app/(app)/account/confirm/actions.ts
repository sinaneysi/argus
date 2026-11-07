"use server";

import { redirect } from "next/navigation";
import { errorOf } from "@/lib/api";
import { safeNext, stepUpPath } from "@/lib/format";
import { apiPost, field, formCsrf, setFlash } from "@/lib/server";

/** A code for this session; the API renews its 2FA time and the new session cookie comes back with the answer. */
export async function stepUpAct(fd: FormData): Promise<void> {
  const next = safeNext(field(fd, "next"), "/today");
  const r = await apiPost<{ ok?: boolean; redirect?: string }>("/api/v1/account/stepup", { code: field(fd, "code").replace(/\s+/g, ""), next }, formCsrf(fd));
  if (r.status === 200 && r.data.ok) {
    await setFlash("Confirmed. Do the action again.", "ok");
    redirect(safeNext(r.data.redirect, next));
  }
  const up = stepUpPath(r.data, next);
  if (up) redirect(up);
  await setFlash(errorOf(r.data, "That did not work."), "fail");
  redirect(`/account/confirm?next=${encodeURIComponent(next)}${field(fd, "sensitive") ? "&sensitive=1" : ""}`);
}

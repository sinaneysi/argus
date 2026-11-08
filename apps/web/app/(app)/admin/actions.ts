"use server";

import { revalidatePath } from "next/cache";
import { redirect } from "next/navigation";
import { errorOf } from "@/lib/api";
import { stepUpPath } from "@/lib/format";
import { apiPost, field, formCsrf, setFlash } from "@/lib/server";

const DEMO = "Disabled in the demo";
const DEMO_WORDS = "Disabled in the demo: nothing was sent and nothing changed.";

type Answer = { ok?: boolean; message?: string; kind?: string; field?: string };
export type GeneralState = { error: string; kind: "fail" | "info"; field: string; values: Record<string, string> };
export type UsersState = { text: string; kind: "ok" | "fail" | "info" };

function values(fd: FormData): Record<string, string> {
  const out: Record<string, string> = {};
  for (const [k, v] of fd.entries()) if (typeof v === "string" && k !== "csrf" && !k.includes("$ACTION")) out[k] = v.slice(0, 2000);
  return out;
}

/** Save and apply: a refusal comes back with every entry kept and the field marked; nothing was written. */
export async function saveGeneral(_: GeneralState | null, fd: FormData): Promise<GeneralState> {
  const v = values(fd);
  const r = await apiPost<Answer>("/api/v1/admin/save", v, formCsrf(fd));
  if (r.status === 200 && r.data.ok) {
    await setFlash(r.data.message ?? "Saved.", "ok");
    redirect("/admin");
  }
  const up = stepUpPath(r.data, "/admin");
  if (up) redirect(up);
  const msg = errorOf(r.data, "Not saved.");
  return { error: msg === DEMO ? DEMO_WORDS : `Not saved: ${msg}`, kind: msg === DEMO ? "info" : "fail", field: r.data.field ?? "", values: v };
}

export async function setClaude(fd: FormData): Promise<void> {
  const r = await apiPost<Answer>("/api/v1/admin/claude", { claude: field(fd, "claude") }, formCsrf(fd));
  if (r.status === 200 && r.data.ok) await setFlash(r.data.message ?? "Saved.", "ok");
  else {
    const up = stepUpPath(r.data, "/admin");
    if (up) redirect(up);
    const msg = errorOf(r.data, "Not saved.");
    await setFlash(msg === DEMO ? DEMO_WORDS : msg, msg === DEMO ? "info" : "fail");
  }
  redirect("/admin");
}

/** Switch an optional module on or off, or back to the configured default (ARGUS_MODULES); applied at once. */
export async function setModule(fd: FormData): Promise<void> {
  const r = await apiPost<Answer>("/api/v1/admin/modules", { module: field(fd, "module"), on: field(fd, "on") }, formCsrf(fd));
  if (r.status === 200 && r.data.ok) await setFlash(r.data.message ?? "Saved.", "ok");
  else {
    const up = stepUpPath(r.data, "/admin");
    if (up) redirect(up);
    const msg = errorOf(r.data, "Not saved.");
    await setFlash(msg === DEMO ? DEMO_WORDS : msg, msg === DEMO ? "info" : "fail");
  }
  redirect("/admin");
}

/** Every write of Users and access. The answer stays in the action's state, never in the flash cookie: it can carry a
 * temporary password, shown once. */
export async function userAct(_: UsersState | null, fd: FormData): Promise<UsersState> {
  const matrix = fd.has("matrix_id");
  const r = await apiPost<Answer>(matrix ? "/api/v1/admin/users/matrix" : "/api/v1/admin/users/act", values(fd), formCsrf(fd));
  const up = stepUpPath(r.data, "/admin/users");
  if (up) redirect(up);
  revalidatePath("/admin/users");
  if (r.status === 200) return { text: r.data.message ?? "Saved.", kind: r.data.kind === "fail" ? "fail" : "ok" };
  const msg = errorOf(r.data, "Not saved.");
  return { text: msg === DEMO ? DEMO_WORDS : msg, kind: msg === DEMO ? "info" : "fail" };
}

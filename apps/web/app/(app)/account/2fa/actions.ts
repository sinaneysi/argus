"use server";

import { redirect } from "next/navigation";
import { errorOf } from "@/lib/api";
import { safeNext, stepUpPath } from "@/lib/format";
import { apiPost, field, formCsrf, setFlash } from "@/lib/server";

const DEMO = "Disabled in the demo";
const DEMO_WORDS = "Disabled in the demo: nothing was sent and nothing changed.";

export type Enrol = { key: string; uri: string; account: string; qr: { n: number; d: string }; ttl: number };
export type Codes = { codes: string[]; title: string; note: string; made: string; next: string };
export type MfaState = { error?: string; kind?: "fail" | "info"; enrol?: Enrol; done?: Codes; next?: string };
type Answer = { ok?: boolean; error?: string; enrol?: Enrol; codes?: string[]; title?: string; note?: string; made?: string; next?: string; message?: string; redirect?: string };

const ROUTES: Record<string, string> = { begin: "/api/v1/account/2fa/begin", confirm: "/api/v1/account/2fa/confirm", disable: "/api/v1/account/2fa/disable", recovery: "/api/v1/account/2fa/recovery" };

// The state of these forms is the one place a seed or a recovery code lives after the API's answer: the page that shows
// it. Never a cookie, never a URL, and nothing here logs it.
export async function mfaAct(_: MfaState | null, fd: FormData): Promise<MfaState> {
  const route = ROUTES[field(fd, "_to")];
  if (!route) return { error: "Unknown form.", kind: "fail" };
  const next = safeNext(field(fd, "next"), "/account");
  const r = await apiPost<Answer>(route, { password: field(fd, "password") || undefined, code: field(fd, "code").replace(/\s+/g, "") || undefined, next }, formCsrf(fd));
  const a = r.data;
  if (r.status === 200 && a.ok) {
    if (a.enrol) return { enrol: a.enrol, next: safeNext(a.next, next) };
    if (a.codes) return { done: { codes: a.codes, title: a.title ?? "", note: a.note ?? "", made: a.made ?? "", next: safeNext(a.next, next) } };
    if (a.message) await setFlash(a.message, "ok");
    redirect(safeNext(a.redirect, "/account/2fa"));
  }
  const up = stepUpPath(a, "/account/2fa");
  if (up) redirect(up);
  const msg = errorOf(a, "That did not work.");
  return { error: msg === DEMO ? DEMO_WORDS : msg, kind: msg === DEMO ? "info" : "fail", enrol: a.enrol, next };
}

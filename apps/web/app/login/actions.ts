"use server";

import { redirect } from "next/navigation";
import { errorOf } from "@/lib/api";
import { safeNext } from "@/lib/format";
import { apiPost, field, formCsrf, setFlash } from "@/lib/server";

type LoginOut = { state?: "ok" | "mfa" | "expired" | "cancelled"; next?: string; error?: string };

/** The password step (also the demo's one-click sign-in: the same form with the public demo password). */
export async function login(fd: FormData): Promise<void> {
  const next = safeNext(field(fd, "next"));
  const r = await apiPost<LoginOut>("/api/v1/auth/login", {
    username: field(fd, "username"), password: field(fd, "password"), remember: field(fd, "remember") === "1" ? "1" : undefined, next,
  }, formCsrf(fd));
  if (r.status === 200 && r.data.state === "mfa") redirect("/login?step=code&next=" + encodeURIComponent(next));
  if (r.status === 200 && r.data.state === "ok") redirect(safeNext(r.data.next ?? next));
  await setFlash(errorOf(r.data, "Sign-in did not work. Try again."), "fail");
  redirect("/login?next=" + encodeURIComponent(next));
}

/** The code step: a code from the authenticator app or a recovery code; "cancel" drops the pending sign-in. */
export async function loginCode(fd: FormData): Promise<void> {
  const next = safeNext(field(fd, "next"));
  const cancel = field(fd, "action") === "cancel";
  const r = await apiPost<LoginOut>("/api/v1/auth/mfa", { code: field(fd, "code").replace(/\s+/g, ""), action: cancel ? "cancel" : undefined }, formCsrf(fd));
  if (r.status === 200 && r.data.state === "ok") redirect(safeNext(r.data.next ?? next));
  if (cancel) redirect("/login?next=" + encodeURIComponent(next));
  await setFlash(errorOf(r.data, "That code did not work."), "fail");
  if (r.data.state === "mfa") redirect("/login?step=code&next=" + encodeURIComponent(next));
  redirect("/login?next=" + encodeURIComponent(next));
}

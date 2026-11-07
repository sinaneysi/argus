"use server";

import { cookies } from "next/headers";
import { redirect } from "next/navigation";
import { errorOf } from "@/lib/api";
import { safeNext } from "@/lib/format";
import { RAIL_COOKIE } from "@/lib/names";
import { apiPost, field, formCsrf, setFlash } from "@/lib/server";

/** The theme switch: light, dark or auto (follow the system); back to the page it was used on. */
export async function setTheme(fd: FormData): Promise<void> {
  const back = safeNext(field(fd, "back"), "/today");
  const r = await apiPost("/api/v1/theme", { theme: field(fd, "theme") }, formCsrf(fd));
  if (r.status !== 200) await setFlash(errorOf(r.data), "fail");
  redirect(back);
}

/** The sidebar as an icon rail or full width: a preference of this browser, so it needs no API call. */
export async function setRail(fd: FormData): Promise<void> {
  const back = safeNext(field(fd, "back"), "/today");
  const jar = await cookies();
  if (field(fd, "rail") === "on") jar.set(RAIL_COOKIE, "on", { path: "/", maxAge: 31536000, sameSite: "lax", httpOnly: true });
  else jar.delete(RAIL_COOKIE);
  redirect(back);
}

export async function logout(fd: FormData): Promise<void> {
  const r = await apiPost<{ redirect?: string }>("/api/v1/auth/logout", {}, formCsrf(fd));
  const to = r.data?.redirect ?? "/login";
  // Keycloak's end-session URL is absolute; anything else stays on this origin
  redirect(to.startsWith("https://") ? to : safeNext(to, "/login"));
}

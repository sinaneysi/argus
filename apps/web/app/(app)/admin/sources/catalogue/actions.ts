"use server";

import { redirect } from "next/navigation";
import { errorOf } from "@/lib/api";
import { stepUpPath } from "@/lib/format";
import { apiPost, field, formCsrf, setFlash } from "@/lib/server";

const PAGE = "/admin/sources/catalogue";
const DEMO = "Disabled in the demo";

/** A route decision or a discovery. The API queues it for root; the page shows the result after the next job. */
export async function catalogueAct(fd: FormData): Promise<void> {
  const action = field(fd, "action");
  const discover = action === "discover";
  if (!discover && !["confirm", "reject", "edit"].includes(action)) redirect(PAGE);
  const r = await apiPost<{ rid?: string; error?: string }>(discover ? "/api/v1/sources/catalogue/discover" : "/api/v1/sources/review/act",
    discover ? { source: field(fd, "source") } : { action, id: field(fd, "id"), reason: field(fd, "reason") || undefined, locator: field(fd, "locator") || undefined, note: field(fd, "note") || undefined }, formCsrf(fd));
  if (r.status === 202) {
    await setFlash(discover ? "Discovery queued." : "Decision queued. It applies at the next job.", "ok");
    redirect(`${PAGE}#decisions`);
  }
  const up = stepUpPath(r.data, PAGE);
  if (up) redirect(up);
  const msg = errorOf(r.data, "Not saved.");
  await setFlash(msg === DEMO ? "Disabled in the demo: nothing was sent and nothing changed." : msg, msg === DEMO ? "info" : "fail");
  redirect(PAGE);
}

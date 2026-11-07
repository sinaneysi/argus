"use server";

import { redirect } from "next/navigation";
import { errorOf } from "@/lib/api";
import { safeNext, stepUpPath } from "@/lib/format";
import { apiPost, formCsrf, setFlash } from "@/lib/server";

// The Marketing section's one write. The API drops the request file the SEO job watches; it never posts to chat itself.

type Answer = { ok?: boolean; redirect?: string };

async function finish(r: { status: number; data: Answer }, here: string): Promise<void> {
  if (r.status === 200 && r.data.ok) redirect(safeNext(r.data.redirect, here));
  const up = stepUpPath(r.data, here);
  if (up) redirect(up);
  const msg = errorOf(r.data, "Not sent.");
  const demo = msg === "Disabled in the demo";
  await setFlash(demo ? "Disabled in the demo: nothing was sent and nothing changed." : msg, demo ? "info" : "fail");
  redirect(here);
}

/** Send today's suggestions to the chat room. */
export async function seoSend(fd: FormData): Promise<void> {
  await finish(await apiPost<Answer>("/api/v1/seo/send", {}, formCsrf(fd)), "/seo");
}

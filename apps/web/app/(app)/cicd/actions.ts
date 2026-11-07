"use server";

import { redirect } from "next/navigation";
import { errorOf } from "@/lib/api";
import { safeNext, stepUpPath } from "@/lib/format";
import { apiPost, field, formCsrf, setFlash } from "@/lib/server";

// The Delivery section's two writes. Each posts to the API, which keeps the old pages' roles, 2FA and request files.

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

/** Read the repo from GitLab again (read-only, the worker does it). */
export async function repoCheck(fd: FormData): Promise<void> {
  const project = field(fd, "project");
  await finish(await apiPost<Answer>("/api/v1/cicd/repo/check", { project }, formCsrf(fd)), `/cicd/repo?project=${encodeURIComponent(project)}`);
}

/** Suppress a vulnerability or remove a rule (admins, a recent 2FA code); applied at the next scan. */
export async function vulnSuppress(fd: FormData): Promise<void> {
  const back = field(fd, "back");
  const body: Record<string, string> = { back };
  for (const k of ["action", "id", "vuln_key", "component", "target", "env", "reason", "expires"]) if (fd.has(k)) body[k] = field(fd, k);
  await finish(await apiPost<Answer>("/api/v1/vulns/suppress", body, formCsrf(fd)), back ? `/vulns?group=${encodeURIComponent(back)}` : "/vulns");
}

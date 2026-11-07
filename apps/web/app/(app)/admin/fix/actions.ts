"use server";

import { redirect } from "next/navigation";
import { errorOf } from "@/lib/api";
import { safeNext, stepUpPath } from "@/lib/format";
import { apiPost, field, formCsrf } from "@/lib/server";

export type FixState = { error: string; kind: "fail" | "info"; prompt: string; hosts: string[] };

const DEMO = "Disabled in the demo";

/** Take action. A refusal comes back as state, so the edited prompt and the chosen hosts stay; the API keeps every check. */
export async function runFix(_: FixState | null, fd: FormData): Promise<FixState> {
  const v = fd.get("prompt");
  const prompt = typeof v === "string" ? v : "";
  const hosts = fd.getAll("host").filter((h): h is string => typeof h === "string");
  const src = { run: field(fd, "run") || undefined, idx: field(fd, "idx") || undefined, rca: field(fd, "rca") || undefined };
  const q = new URLSearchParams(Object.entries(src).filter((e): e is [string, string] => !!e[1])).toString();
  const r = await apiPost<{ ok?: boolean; redirect?: string }>("/api/v1/fix/review", { ...src, prompt, host: hosts, reviewed: field(fd, "reviewed") || undefined }, formCsrf(fd));
  if (r.status === 200 && r.data.ok) redirect(safeNext(r.data.redirect, "/admin/fix"));
  const up = stepUpPath(r.data, "/admin/fix/review?" + q);
  if (up) redirect(up);
  const msg = errorOf(r.data, "Not started.");
  return { error: msg === DEMO ? "Disabled in the demo: nothing was sent and nothing changed." : msg, kind: msg === DEMO ? "info" : "fail", prompt, hosts };
}

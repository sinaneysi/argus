"use server";

import { redirect } from "next/navigation";
import { errorOf } from "@/lib/api";
import { safeNext, stepUpPath } from "@/lib/format";
import { apiPost, field, formCsrf, setFlash } from "@/lib/server";

const DEMO = "Disabled in the demo";
const DEMO_WORDS = "Disabled in the demo: nothing was sent and nothing changed.";

// The Settings writes: which API route a form may reach, and the page it comes back to. A form names one of these keys;
// nothing else is accepted, so a posted name can never choose another path.
const ROUTES: Record<string, [api: string, page: string]> = {
  board: ["/api/v1/admin/board", "/admin/board"],
  "jira-hosts": ["/api/v1/admin/jira-hosts", "/admin/board#hosts"],
  "jira-shared": ["/api/v1/admin/jira-shared", "/admin/board#shared"],
  agent: ["/api/v1/admin/agent", "/admin/board#agent"],
  "fixer-move": ["/api/v1/admin/agent/fixer-move", "/admin/board#agent"],
  "claude-token": ["/api/v1/admin/claude-token", "/admin/claude#claude-token"],
  "claude-check": ["/api/v1/admin/claude-token", "/admin/claude#claude-egress"], // Check now is an action of the token route
  "claude-egress": ["/api/v1/admin/claude-egress", "/admin/claude#claude-egress"],
  kube: ["/api/v1/admin/kube", "/admin/kube"],
  sources: ["/api/v1/admin/sources", "/admin/sources"],
  "backup-targets": ["/api/v1/admin/backup-targets", "/admin/backup-targets"],
  outline: ["/api/v1/admin/outline", "/admin/outline"],
  account: ["/api/v1/account", "/account"],
  "account-jira": ["/api/v1/account/jira", "/account#jira"],
  "account-matrix": ["/api/v1/account/matrix", "/account#matrix"],
  "account-sessions": ["/api/v1/account/sessions", "/account"],
  gitlab: ["/api/v1/admin/gitlab-issues", "/admin/outline#gitlab"],
  "claude-usage": ["/api/v1/admin/claude", "/admin/claude#claude-usage"],
};

type Answer = { ok?: boolean; redirect?: string; message?: string; field?: string };
export type SetState = { error: string; kind: "fail" | "info"; field: string; values: Record<string, string> };

/** Entries that never come back to the form after a refusal: the names of fields that hold a secret. Whole names only, so
 * a switch such as "secrets" keeps its state. */
const SECRET = /^(.*_)?(token|password|secret|key|kubeconfig|credential|code|file|current|new|new2)$/i;

type Entries = Record<string, string | string[]>;

/** The form's entries; a chosen file goes as its text, like the Go route's upload (the API limits the size). A name posted
 * twice (a group of checkboxes) goes as a list. */
async function entries(fd: FormData): Promise<Entries> {
  const out: Entries = {};
  for (const [k, v] of fd.entries()) {
    if (k === "csrf" || k === "_to" || k === "who" || k.includes("$ACTION")) continue;
    const text = typeof v === "string" ? v : v.size > 0 ? await v.text() : null;
    if (text === null) continue;
    const had = out[k];
    out[k] = had === undefined ? text : [...(Array.isArray(had) ? had : [had]), text];
  }
  return out;
}

const kept = (v: Entries) => Object.fromEntries(Object.entries(v).filter(([k]) => !SECRET.test(k)).map(([k, x]) => [k, Array.isArray(x) ? x.join(",") : x]));

/** A form with entries: success lands on the page the API names; a refusal keeps the form open with its field marked. */
export async function settingsAct(_: SetState | null, fd: FormData): Promise<SetState> {
  const route = ROUTES[field(fd, "_to")];
  const v = await entries(fd);
  if (!route) return { error: "Not saved: unknown form.", kind: "fail", field: "", values: kept(v) };
  const [api, page] = route;
  const r = await apiPost<Answer>(api, v, formCsrf(fd));
  if (r.status === 200 && r.data.ok) {
    if (r.data.message) await setFlash(r.data.message, "ok");
    redirect(safeNext(r.data.redirect, page));
  }
  const up = stepUpPath(r.data, page);
  if (up) redirect(up);
  const msg = errorOf(r.data, "Not saved.");
  return { error: msg === DEMO ? DEMO_WORDS : `Not saved: ${msg}`, kind: msg === DEMO ? "info" : "fail", field: r.data.field ?? "", values: kept(v) };
}

/** A button with no entries to keep (sync now, disconnect): the answer is a flash on the page. */
export async function settingsGo(fd: FormData): Promise<void> {
  const route = ROUTES[field(fd, "_to")];
  if (!route) redirect("/admin");
  const [api, page] = route;
  const r = await apiPost<Answer>(api, await entries(fd), formCsrf(fd));
  if (r.status === 200 && r.data.ok) {
    if (r.data.message) await setFlash(r.data.message, "ok");
    redirect(safeNext(r.data.redirect, page));
  }
  const up = stepUpPath(r.data, page);
  if (up) redirect(up);
  const msg = errorOf(r.data, "Not saved.");
  await setFlash(msg === DEMO ? DEMO_WORDS : msg, msg === DEMO ? "info" : "fail");
  redirect(page);
}

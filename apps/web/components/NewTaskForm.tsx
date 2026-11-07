"use client";

import { useActionState } from "react";
import { Banner } from "./Banner";
import { createTask, type CreateState } from "@/app/(app)/board/actions";
import type { NewTask } from "@/lib/types";
import { Button } from "@/components/ui/button";
import { PlainCard } from "@/components/ui/card";

/** The one client part of New task: a refused save re-renders with what was typed. Without JavaScript the form still posts. */
export function NewTaskForm({ d, csrf }: { d: NewTask; csrf: string }) {
  const back = new URLSearchParams();
  if (d.src) { back.set("src", d.src); back.set("id", d.srcId ?? ""); }
  if (d.return) back.set("return", d.return);
  const q = back.toString();
  const [state, action, busy] = useActionState<CreateState | null, FormData>(createTask, null, "/board/new" + (q ? "?" + q : ""));
  const v = state?.values ?? d.values;
  const t = d.trackers;
  const cur = v.tracker === "gitlab" ? "gitlab" : "jira";
  return (
    <PlainCard as="form" action={action} className="tf">
      <input type="hidden" name="csrf" value={csrf} />
      {d.src ? <><input type="hidden" name="src" value={d.src} /><input type="hidden" name="id" value={d.srcId} /></> : null}
      {d.return ? <input type="hidden" name="return" value={d.return} /> : null}
      {t ? (
        <fieldset className="tf-trk"><legend>Create in</legend>
          <label className="check"><input type="radio" name="tracker" value="jira" defaultChecked={cur === "jira"} /><span><b>Jira</b> <small className="muted">{t.jira}</small></span></label>
          <label className="check"><input type="radio" name="tracker" value="gitlab" defaultChecked={cur === "gitlab"} /><span><b>GitLab issues</b> <small className="muted">{t.gitlab}</small></span></label>
          {t.projects.length ? <label className="field"><span>GitLab project</span><select key={v.gl_project} name="gl_project" defaultValue={v.gl_project || t.project}>{t.projects.map((o) => <option key={o.value} value={o.value}>{o.label}</option>)}</select></label> : null}
        </fieldset>
      ) : null}
      <label className="field"><span>Title</span><input name="title" defaultValue={v.title} maxLength={d.limits.title} required dir="auto" /></label>
      <div className="tf-row">
        <label className="field"><span>Environment</span><select key={v.env} name="env" defaultValue={v.env}>{d.envs.map((o) => <option key={o.value} value={o.value}>{o.label}</option>)}</select></label>
        <label className="field"><span>Host</span><input name="host" defaultValue={v.host} maxLength={80} placeholder="name or address" /></label>
        <label className="field"><span>Priority</span><select key={v.priority} name="priority" defaultValue={v.priority}>{d.priorities.map((p) => <option key={p}>{p}</option>)}</select></label>
        <label className="field"><span>Labels</span><input name="labels" defaultValue={v.labels} maxLength={300} placeholder="space separated" /></label>
      </div>
      <label className="field"><span>Description</span><textarea name="description" rows={3} maxLength={d.limits.text} defaultValue={v.description} dir="auto" /></label>
      <label className="field"><span>Evidence</span><textarea name="evidence" rows={10} className="mono" maxLength={d.limits.text} defaultValue={v.evidence} /></label>
      <label className="field"><span>Fix prompt</span><small>For the fixer on develop or pre-release; a proposal for a human on production.</small>
        <textarea name="fix_prompt" rows={12} className="mono" maxLength={d.limits.prompt} defaultValue={v.fix_prompt} /></label>
      <div className="tf-foot">
        {state ? <Banner b={{ text: state.error, kind: state.kind }} /> : null}
        <div className="tf-save"><Button  disabled={busy}>{busy ? "Creating…" : "Create task"}</Button><span className="muted small">Agents only propose: the task waits in To do until a person moves it.</span></div>
      </div>
    </PlainCard>
  );
}

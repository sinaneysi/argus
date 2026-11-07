"use client";

import { Fragment, useActionState } from "react";
import { Banner } from "./Banner";
import { lessonEdit, type LessonEditState } from "@/app/(app)/review/actions";
import type { LessonRow, Lessons } from "@/lib/types";
import { Button } from "@/components/ui/button";

/** Edit and approve: a refused save re-renders with what was typed. Without JavaScript the form still posts. */
export function LessonEditForm({ l, d, csrf }: { l: LessonRow; d: Lessons; csrf: string }) {
  const [state, action, busy] = useActionState<LessonEditState | null, FormData>(lessonEdit, null, d.here + `#l${l.id}`);
  const v = state?.values ?? { title: l.title, body: l.body, service: l.service, env: l.env, kind: l.kind, tags: l.tags.join(" ") };
  const opts = (list: string[]) => list.map((o) => <option key={o} value={o}>{o || "Not set"}</option>);
  return (
    <details className="fold" open={state ? true : undefined}><summary>Edit and approve</summary>
      <form action={action} className="fl-form">
        <input type="hidden" name="csrf" value={csrf} /><input type="hidden" name="id" value={l.id} /><input type="hidden" name="action" value="edit" /><input type="hidden" name="return" value={d.here} />
        {state ? <Banner b={{ text: state.error, kind: state.kind }} /> : null}
        <Fragment key={JSON.stringify(v)}>
          <label className="field"><span>Title</span><small>1–{d.max.title} characters</small><input name="title" dir="auto" maxLength={d.max.title} required defaultValue={v.title} /></label>
          <label className="field"><span>Lesson</span><small>1–{d.max.body} characters</small><textarea name="body" className="ta" dir="auto" rows={6} maxLength={d.max.body} required defaultValue={v.body} /></label>
          <div className="fl-grid">
            <label className="field"><span>Service (GitLab path)</span><input name="service" maxLength={300} placeholder="group/project" defaultValue={v.service} /></label>
            <label className="field"><span>Environment</span><select name="env" defaultValue={v.env}>{opts(d.envs)}</select></label>
            <label className="field"><span>Kind</span><select name="kind" defaultValue={v.kind}>{opts(d.kinds)}</select></label>
            <label className="field"><span>Tags</span><input name="tags" maxLength={250} placeholder="Up to 6, space separated" defaultValue={v.tags} /></label>
          </div>
        </Fragment>
        <div className="fl-acts"><Button variant="outline" size="sm" disabled={busy}>{busy ? "Saving…" : "Save and approve"}</Button><span className="muted small">Root re-checks and secret-scans the text before the lesson is approved.</span></div>
      </form>
    </details>
  );
}

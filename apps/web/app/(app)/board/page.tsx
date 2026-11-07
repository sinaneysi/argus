import { HelpQ } from "@/components/HelpQ";
import type { Metadata } from "next";
import { boardRefresh, matrixPreview } from "./actions";
import { Reload } from "@/components/Infra";
import { Banners, one, type Search, Segs } from "@/components/Section";
import { BoardChips, Columns, MoveChoiceForm } from "@/components/Work";
import { Fragment } from "react";
import { apiGet, csrfToken, readFlash } from "@/lib/server";
import type { Board } from "@/lib/types";
import { Button, buttonVariants } from "@/components/ui/button";
import { PlainCard } from "@/components/ui/card";

export const metadata: Metadata = { title: "Tasks" };

export default async function BoardPage({ searchParams }: { searchParams: Search }) {
  const sp = await searchParams;
  const u = new URLSearchParams();
  for (const k of ["show", "view", "tracker", "env", "assignee", "label", "source", "all", "req"]) if (one(sp[k])) u.set(k, one(sp[k]));
  const qf = sp.qf;
  for (const x of Array.isArray(qf) ? qf : qf ? [qf] : []) u.append("qf", x);
  const q = u.toString();
  const [r, csrf, flash] = await Promise.all([apiGet<Board>("/api/v1/board" + (q ? "?" + q : "")), csrfToken(), readFlash()]);
  if (r.status !== 200) throw new Error(`/api/v1/board answered ${r.status}`);
  const d = r.data;
  return (
    <>
      <Reload r={d.refresh} />
      <header className="phead">
        <div><h1>Tasks</h1><HelpQ p="/board" />
          <p className="status-line">{d.status.map((s, i) => <Fragment key={i}>{i ? <span className="sep">·</span> : null}<Segs s={s} /></Fragment>)}</p>
          {d.boardError ? <p className="small fail-t">{d.boardError}</p> : null}</div>
        <div className="phead-act actionbar">
          {d.canRefresh ? <form action={boardRefresh}><input type="hidden" name="csrf" value={csrf} /><input type="hidden" name="return" value={d.return} /><Button variant="outline">Refresh now</Button></form> : null}
          {d.canCreate ? <a data-slot="button" className={buttonVariants()} href={d.newHref}>New task</a> : null}
          {d.ask ? <a data-slot="button" className={buttonVariants({ variant: "outline" })} href="/board/ask">Ask Argus</a> : null}
        </div>
      </header>
      {d.tabs.length ? <nav className="tabs" aria-label="Tracker">{d.tabs.map((t) => <a key={t.href} href={t.href} className={t.current ? "on" : undefined} aria-current={t.current ? "page" : undefined}>{t.label}</a>)}</nav> : null}
      <Banners list={d.notices} flash={flash} />
      {d.choice ? <MoveChoiceForm c={d.choice} csrf={csrf} ret={d.return} /> : null}
      {d.matrix.length ? (
        <PlainCard as="section" className="l-card" aria-labelledby="mx-h">
          <div className="card-h rows-h"><h2 id="mx-h">Waiting for your OK (Matrix) <span className="ncount">{d.matrix.length}</span></h2><span className="muted small">Not in Jira yet. Answer in the DM or here.</span></div>
          <ul className="rows">{d.matrix.map((m) => (
            <li key={m.pid} className="mx-r">
              <div><p dir="auto"><b>{m.title}</b></p>
                <p className="muted small">Requester: {m.requester} · forwarded {m.forwarded} ago · expires in {m.hoursLeft} h · <span className="mono">{m.project}</span> · Task</p>
                {m.summary ? <p className="small" dir="auto">{m.summary}</p> : null}</div>
              <form action={matrixPreview} className="mx-f"><input type="hidden" name="csrf" value={csrf} /><input type="hidden" name="return" value={d.return} /><input type="hidden" name="pid" value={m.pid} />
                <label className="field"><span className="sr">Assignee</span><select name="assignee" defaultValue={m.assignee}>{m.options.map((o) => <option key={o.value} value={o.value}>{o.label}</option>)}</select></label>
                <Button variant="outline" size="sm" name="action" value="create">Create</Button><Button variant="destructive" size="sm" name="action" value="discard">Discard</Button></form>
            </li>
          ))}</ul>
        </PlainCard>
      ) : null}
      {d.local ? <p className="muted small note">{d.local}{d.connectHref ? <> <a href={d.connectHref}>Connect Jira</a>.</> : null}</p> : null}
      {d.readOnly ? <p className="muted small note">{d.readOnly}</p> : null}
      <BoardChips d={d} />
      {d.note ? <p className="muted small note">{d.note}</p> : null}
      {d.emptyAll ? <PlainCard as="p" className="empty"><b>{d.emptyAll.title}</b> {d.emptyAll.text}</PlainCard> : <Columns d={d} csrf={csrf} />}
    </>
  );
}

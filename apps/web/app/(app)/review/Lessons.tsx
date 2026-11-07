import { HelpQ } from "@/components/HelpQ";
import { lessonAct, lessonSettings } from "./actions";
import "./queue.css";
import { Reload } from "@/components/Infra";
import { LessonEditForm } from "@/components/LessonEditForm";
import { EnvPill, Pill } from "@/components/Pills";
import { Banners, Crumb, Filters, Hidden, Pager, tone, When } from "@/components/Section";
import { AdminsOnly } from "@/components/Agent";
import { qs } from "@/lib/api";
import { plural } from "@/lib/format";
import { apiGet, csrfToken, readFlash } from "@/lib/server";
import type { LessonRow, Lessons } from "@/lib/types";
import { Button } from "@/components/ui/button";
import { PlainCard } from "@/components/ui/card";

const ACT: Record<string, { label: string; variant: "outline" | "destructive" }> = {
  approve: { label: "Approve", variant: "outline" }, reject: { label: "Reject", variant: "destructive" },
  retire: { label: "Retire", variant: "destructive" }, review: { label: "Back to review", variant: "outline" },
};

const DANGER = ["reject", "retire"];

function Row({ l, d, csrf }: { l: LessonRow; d: Lessons; csrf: string }) {
  const act = (a: string) => <form key={a} action={lessonAct}><Hidden csrf={csrf} id={l.id} action={a} return={d.here} /><Button variant={ACT[a]?.variant} size="sm">{ACT[a]?.label}</Button></form>;
  return (
    <li id={`l${l.id}`} className="fl-row">
      <div className="fl-main">
        <div className="fl-t"><Pill tone={tone(l.stateKind)}>{l.state}</Pill><b dir="auto">{l.title}</b>
          {l.review === "update" ? <Pill tone="warn">update of lesson {l.supersedes}</Pill> : null}
          {l.review === "retire?" ? <><Pill tone="warn">retire?</Pill><span className="muted small">unused for a long time or flagged as not helpful</span></> : null}</div>
        <div className="fl-m">
          {l.service ? <span className="mono">{l.service}</span> : null}{l.env ? <EnvPill env={l.env} /> : null}{l.kind ? <span>{l.kind}</span> : null}
          {l.tags.map((t) => <span key={t}>{t}</span>)}
          <span>from {l.source.href ? <a href={l.source.href}>{l.source.text}</a> : l.source.text}</span>
          <span>proposed <When ts={l.proposedAt} /></span>
          {l.used ? <span>used {l.used.uses} ×{l.used.last ? <>, last <When ts={l.used.last} /></> : null}</span> : null}
          {l.decided ? <span>{l.state} by {l.decided.by} <When ts={l.decided.at} /></span> : null}
        </div>
        <p className="fl-body" dir="auto">{l.body}</p>
        {l.evidence ? <p className="muted small" dir="auto">Evidence: {l.evidence}</p> : null}
        <div className="fl-acts">
          {l.actions.filter((a) => !DANGER.includes(a)).map(act)}
          {l.editable ? <LessonEditForm l={l} d={d} csrf={csrf} /> : null}
          {l.actions.filter((a) => DANGER.includes(a)).map(act)}
        </div>
      </div>
    </li>
  );
}

/** Review's full list of lessons: the four states, the filters, Edit and approve, and the on/off switch. */
export async function LessonsFull({ params }: { params: Record<string, string> }) {
  const [r, csrf, flash] = await Promise.all([apiGet<Lessons>("/api/v1/lessons" + qs(params)), csrfToken(), readFlash()]);
  if (r.status === 403) return <><header className="phead"><div><Crumb href="/review?kind=lessons" label="Review" /><h1>Lessons</h1><HelpQ p="/review" /></div></header><AdminsOnly /></>;
  if (r.status !== 200) throw new Error(`/api/v1/lessons answered ${r.status}`);
  const d = r.data;
  return (
    <>
      <Reload r={d.refresh} />
      <header className="phead"><div><Crumb href="/review?kind=lessons" label="Review" /><h1>Lessons</h1><HelpQ p="/review" />
        <p className="meta-line"><b>{d.waiting ? `${plural(d.waiting, "lesson")} waiting for review` : "Nothing waiting"}</b> · {d.on ? "only approved lessons reach a prompt" : <span className="r-denied">lessons off: none proposed or loaded</span>}</p></div></header>
      <Banners list={d.banners} flash={flash} />
      <div className="stack">
        <nav className="tabs" aria-label="Lesson views">{d.tabs.map((t) => <a key={t.href} href={t.href} className={t.current ? "on" : undefined} aria-current={t.current ? "page" : undefined}>{t.label} <span className="ncount">{t.count}</span></a>)}</nav>
        <Filters list={d.list} facets={d.facets} label="Filter lessons" placeholder="Title, lesson, service, tag or task" />
        <PlainCard as="section" className="l-card" aria-labelledby="ls-h"><div className="card-h pad"><h2 id="ls-h">{d.title} <span className="ncount">{d.list.matched}</span></h2><a className="small" href="/knowledge">What Argus knows</a></div>
          {d.rows.length ? <ul className="rows">{d.rows.map((l) => <Row key={l.id} l={l} d={d} csrf={csrf} />)}</ul>
            : d.empty ? <p className="empty pad">{d.empty}</p>
              : <p className="empty pad">No lesson matches these filters. <a href={d.list.clearHref}>Clear the filters</a></p>}
          <Pager list={d.list} />
        </PlainCard>
        <PlainCard as="details" className="fold-t"><summary>Settings <span className="muted">{d.on ? "on" : "off"}</span></summary>
          <form action={lessonSettings} className="fl-form"><Hidden csrf={csrf} return={d.here} />
            <label className="check"><input type="checkbox" name="enabled" value="1" defaultChecked={d.on} /><span>Propose and load lessons</span></label>
            <p className="muted small">Off: runs propose nothing and no lesson is loaded into a prompt. The lessons stay.</p>
            <div className="fl-acts"><Button variant="outline">Save setting</Button><span className="muted small">The board sync applies it; the result shows at the top of the page.</span></div>
          </form></PlainCard>
      </div>
    </>
  );
}

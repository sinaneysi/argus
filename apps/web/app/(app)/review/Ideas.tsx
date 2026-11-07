import { HelpQ } from "@/components/HelpQ";
import { ideaAct, ideaGenerate } from "./actions";
import "./queue.css";
import { Reload } from "@/components/Infra";
import { Pill } from "@/components/Pills";
import { Banners, Crumb, Filters, Hidden, Pager, tone, When } from "@/components/Section";
import { qs } from "@/lib/api";
import { apiGet, csrfToken, readFlash } from "@/lib/server";
import type { IdeaRow, Ideas } from "@/lib/types";
import { Button } from "@/components/ui/button";
import { PlainCard } from "@/components/ui/card";

function Acts({ i, csrf, here, reason }: { i: IdeaRow; csrf: string; here: string; reason: Ideas["reason"] }) {
  const has = (a: string) => i.actions.includes(a);
  const hid = <Hidden csrf={csrf} id={i.id} here={here} />;
  return (
    <>
      {has("explore") ? <form action={ideaAct}>{hid}<input type="hidden" name="action" value="explore" /><Button variant="outline" size="sm" title="Moves it to Under review while you look into it">Mark under review</Button></form> : null}
      {has("accept") ? <form action={ideaAct}>{hid}<input type="hidden" name="action" value="accept" /><Button variant="outline" size="sm" title="Argus writes a build prompt for Claude Code on this row; nothing is built or deployed">Accept &amp; write build prompt</Button></form> : null}
      {has("built") ? <details className="fold"><summary>Mark built</summary><form action={ideaAct} className="fl-in">{hid}<input type="hidden" name="action" value="built" />
        <label className="sr" htmlFor={`ib-${i.id}`}>Commit or merge request</label><input id={`ib-${i.id}`} type="text" name="ref" maxLength={300} required placeholder="Commit hash, !MR or https link" /><Button variant="outline" size="sm">Mark built</Button></form></details> : null}
      {has("reject") ? <details className="fold"><summary>Reject</summary><form action={ideaAct} className="fl-in">{hid}<input type="hidden" name="action" value="reject" />
        <label className="sr" htmlFor={`ir-${i.id}`}>Reason</label><input id={`ir-${i.id}`} type="text" name="reason" minLength={reason.min} maxLength={reason.max} required placeholder="Reason (required, remembered)" /><Button variant="destructive" size="sm">Reject</Button></form></details> : null}
    </>
  );
}

function Row({ i, csrf, here, reason }: { i: IdeaRow; csrf: string; here: string; reason: Ideas["reason"] }) {
  return (
    <li id={`i-${i.id}`} className="fl-row">
      <span className="fl-av" title={i.adviserRole ? `${i.adviser}, ${i.adviserRole}` : i.adviser} aria-hidden="true">{i.initial}</span>
      <div className="fl-main">
        <div className="fl-t"><Pill tone={tone(i.status.kind)}>{i.status.text}</Pill><b dir="auto">{i.title}</b><When ts={i.at} /></div>
        <div className="fl-m"><span>{i.area}</span><span>{i.adviser}</span><span title="Effort: S under a week, M one to three weeks, L more">effort {i.effort}</span>{i.revives ? <span>returns after rejected #{i.revives}</span> : null}</div>
        {i.value ? <p className="small" dir="auto">Value: {i.value}</p> : null}
        {i.wait ? <p className="muted small">The build prompt is being written; it appears here within a minute.</p> : null}
        <div className="fl-acts">
          <details className="fold"><summary>Details</summary>
            {i.evidence.length ? <ul className="fl-ev">{i.evidence.map((e, n) => <li key={n} dir="auto">{e.href ? <a href={e.href}>{e.text}</a> : e.text}</li>)}</ul> : null}
            <dl className="fl-kv">{i.facts.map((f) => <div key={f.label}><dt>{f.label}</dt><dd dir="auto">{f.value}</dd></div>)}
              {i.built ? <div><dt>Built</dt><dd>{i.built.href ? <a href={i.built.href} rel="noopener noreferrer">{i.built.ref}</a> : <span className="mono">{i.built.ref}</span>} · {i.built.by}, {i.built.when}</dd></div> : null}</dl>
          </details>
          {i.prompt ? <details className="fold"><summary>Build prompt for Claude Code</summary>
            {i.promptHref ? <p className="small"><a href={i.promptHref} download>Download .md</a></p> : null}
            <pre className="code prompt" role="region" aria-label="Build prompt" tabIndex={0}><code>{i.prompt}</code></pre>
            {i.refined ? <p className="muted small">Refined by one Sonnet pass; every guardrail and path was checked.</p> : null}</details> : null}
          <Acts i={i} csrf={csrf} here={here} reason={reason} />
        </div>
      </div>
    </li>
  );
}

/** Review's full list of ideas: every status, the filters, the decisions and the advisers. */
export async function IdeasFull({ params }: { params: Record<string, string> }) {
  const [r, csrf, flash] = await Promise.all([apiGet<Ideas>("/api/v1/ideas" + qs(params)), csrfToken(), readFlash()]);
  if (r.status !== 200) throw new Error(`/api/v1/ideas answered ${r.status}`);
  const d = r.data;
  const here = "/review" + qs(params);
  return (
    <>
      <Reload r={d.refresh} />
      <header className="phead"><div><Crumb href="/review?kind=ideas" label="Review" /><h1>Ideas</h1><HelpQ p="/review" /><p className="meta-line">{d.line.map((l, n) => <span key={n}>{n ? "· " : ""}{l}</span>)}</p></div></header>
      <Banners list={d.banners} flash={flash} />
      <div className="stack">
        {d.total > 0 ? <>
          <nav className="tabs" aria-label="Idea views">{d.tabs.map((t) => <a key={t.href} href={t.href} className={t.current ? "on" : undefined} aria-current={t.current ? "page" : undefined}>{t.label} <span className="ncount">{t.count}</span></a>)}</nav>
          <Filters list={d.list} facets={d.facets} label="Filter ideas" placeholder="Title, problem, proposal, value, evidence" />
        </> : null}
        <PlainCard as="section" className="l-card" aria-labelledby="il-h"><div className="card-h pad"><h2 id="il-h">{d.heading} <span className="ncount">{d.list.matched}</span></h2></div>
          {d.rows.length ? <ul className="rows">{d.rows.map((i) => <Row key={i.id} i={i} csrf={csrf} here={here} reason={d.reason} />)}</ul>
            : d.empty ? <p className="empty pad">{d.empty}{d.total ? <> <a href="/review?kind=ideas&mode=full&status=all">Show all ideas</a></> : null}</p>
              : <p className="empty pad">No idea matches these filters. <a href={d.list.clearHref}>Clear the filters</a></p>}
          <Pager list={d.list} />
        </PlainCard>
        <PlainCard as="section" className="l-card" aria-labelledby="iw-h"><div className="card-h pad"><h2 id="iw-h">Advisers</h2></div>
          <p className="card-sub pad">Each proposes up to 5 ideas once a week; Generate now runs one at most once a day.</p>
          {d.advisers.length ? <ul className="rows">{d.advisers.map((a) => (
            <li key={a.key} className="fl-row"><span className="fl-av" aria-hidden="true">{a.initial}</span><div className="fl-main"><b>{a.name}</b><div className="fl-m">{a.meta.map((m, n) => <span key={n}>{m}</span>)}</div></div>
              {a.can ? <form action={ideaGenerate}><Hidden csrf={csrf} persona={a.key} here={here} /><Button variant="outline" size="sm">Generate now</Button></form> : a.why ? <span className="muted small">{a.why}</span> : null}</li>
          ))}</ul> : <p className="empty pad">No advisers yet: the first run of the ideas job writes its status.</p>}
        </PlainCard>
      </div>
    </>
  );
}

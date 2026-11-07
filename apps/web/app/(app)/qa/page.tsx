import { HelpQ } from "@/components/HelpQ";
import type { Metadata } from "next";
import { Fragment } from "react";
import { problemAct } from "../incidents/actions";
import { EnvPill, Pill } from "@/components/Pills";
import { Banners, Filters, Hidden, one, Pager, type Search, Tiles, tone, When } from "@/components/Section";
import { Inline } from "@/components/Markdown";
import { Chain, Disp, MsgBody, Sender } from "@/components/Work";
import "@/components/work.css";
import { apiGet, csrfToken, readFlash } from "@/lib/server";
import { full, iso } from "@/lib/format";
import type { Qa, QaIssue } from "@/lib/types";
import { Button } from "@/components/ui/button";
import { PlainCard } from "@/components/ui/card";

export const metadata: Metadata = { title: "Conversations" };

function Issue({ i, bulk }: { i: QaIssue; bulk: boolean }) {
  const a = i.ask;
  return (
    <li id={`issue-${i.id}`} className="q-i">
      <div className="q-h">
        {bulk ? <label className="l-hit"><input className="l-pick" type="checkbox" name="ref" value={`qa_issue:${i.id}`} form="pblink" aria-label={`Select QA-${i.id}`} /></label> : null}
        {i.state ? <Pill tone={tone(i.state.kind)}>{i.state.text}</Pill> : null}
        <b dir="auto">{i.title}</b>
        {i.problem ? <a className="chip" href={`/problems/view?id=${i.problem.id}`} title={i.problem.title}>{i.problem.label}</a> : null}
        {i.env ? <EnvPill env={i.env} /> : null}{i.severity ? <span className="small muted">{i.severity}</span> : null}
        {i.confirmed ? <Pill title="Argus's own alerts or logs confirmed the fault">confirmed by evidence</Pill> : null}{i.suspicious ? <Pill>suspicious</Pill> : null}
        {i.reconstructed ? <Pill title="rebuilt from the recorded messages">reconstructed</Pill> : null}
      </div>
      <p className="muted small">{i.who ? <span dir="auto">{i.who} · </span> : null}{i.span}{i.missing.length ? ` · missing: ${i.missing.join(", ")}` : null}</p>
      {i.lazy ? <details className="fold"><summary>Summary <span className="muted">{i.lazy.source}</span></summary><dl className="rec wide">{i.lazy.rows.map((x) => <div key={x.label}><dt>{x.label}</dt><dd dir="auto"><Inline text={x.value} codeOnly /></dd></div>)}<div><dt>Fix status</dt><dd><Pill tone={tone(i.lazy.statusKind)}>{i.lazy.status}</Pill></dd></div></dl></details> : null}
      {i.chain.length ? <Chain c={i.chain} /> : null}
      {a?.note ? <p className="muted small">{a.note}</p> : null}
      {a?.head ? (
        <div className="q-ask"><p className="small">{a.head}{a.link ? <> · <a href={a.link} rel="noopener noreferrer">Open in Matrix</a></> : null}</p>
          {a.text ? <blockquote dir="auto">{a.text}</blockquote> : null}
          {a.answered ? <Pill tone="ok">{a.answered}</Pill> : a.tail ? <p className="muted small">{a.tail}</p> : null}</div>
      ) : null}
      <details className="fold"><summary>{i.count} message{i.count === 1 ? "" : "s"}</summary>
        <ul className="q-msgs">{i.messages.map((m) => (
          <li key={m.id}><blockquote><MsgBody m={m} /></blockquote>
            <p className="muted small"><span dir="auto">{m.sender}</span> · <time dateTime={iso(m.at)}>{full(m.at)}</time>{m.link ? <> · <a href={m.link} rel="noopener noreferrer">Open in Matrix</a></> : null}{m.disp ? <> · {m.disp.text}</> : null}</p></li>
        ))}</ul>
        {i.older ? <p className="muted small">{i.older} older {i.older === 1 ? "message" : "messages"} not shown.</p> : null}
      </details>
    </li>
  );
}

export default async function QaPage({ searchParams }: { searchParams: Search }) {
  const sp = await searchParams;
  const u = new URLSearchParams();
  for (const k of ["view", "q", "state", "env", "disp", "sender", "has", "from", "to", "page"]) if (one(sp[k])) u.set(k, one(sp[k]));
  const q = u.toString();
  const [r, csrf, flash] = await Promise.all([apiGet<Qa>("/api/v1/qa" + (q ? "?" + q : "")), csrfToken(), readFlash()]);
  if (r.status !== 200) throw new Error(`/api/v1/qa answered ${r.status}`);
  const d = r.data;
  const label = d.view === "messages" ? "Search messages" : "Search issues";
  const ph = d.view === "messages" ? "Words in the message or the sender's name" : "Title, reporter, QA-n or environment";
  return (
    <>
      <header className="phead"><div><h1>Conversations</h1><HelpQ p="/qa" /><p className="status-line">{d.status.map((s, i) => <Fragment key={s}>{i ? <span className="sep">·</span> : null}{i ? s : <b>{s}</b>}</Fragment>)}</p></div></header>
      <nav className="tabs" aria-label="View">{d.tabs.map((t) => <a key={t.href} href={t.href} className={t.current ? "on" : undefined} aria-current={t.current ? "page" : undefined}>{t.label}{t.count != null ? <> <span className="num">{t.count}</span></> : null}</a>)}</nav>
      <Banners flash={flash} />
      {!d.ready ? <PlainCard as="section"><p className="empty"><b>Nothing yet.</b> {d.empty}</p></PlainCard> : null}
      {d.tiles ? <Tiles tiles={d.tiles} /> : null}
      {d.list && d.facets ? (
        <Filters list={d.list} facets={d.facets} label={label} placeholder={ph}>
          {d.dates ? <>
            <label className={d.dates.from ? "f-pill on" : "f-pill"}><span className="f-l">From</span><input type="date" name="from" defaultValue={d.dates.from} /></label>
            <label className={d.dates.to ? "f-pill on" : "f-pill"}><span className="f-l">To</span><input type="date" name="to" defaultValue={d.dates.to} /></label>
          </> : null}
        </Filters>
      ) : null}
      {d.ready && d.view === "issues" ? (
        <PlainCard as="section" className="l-card" aria-label="Issues">
          {d.rows?.length ? <ul className="rows">{d.rows.map((i) => <Issue key={i.id} i={i} bulk={!!d.bulk} />)}</ul>
            : d.list?.filtered ? <p className="empty pad">No issue matches these filters. <a href={d.list.clearHref}>Clear the filters</a></p> : <p className="empty pad">{d.empty}</p>}
          {d.bulk ? (
            <form id="pblink" action={problemAct} className="l-bulk">
              <Hidden csrf={csrf} action="link" link_type="caused_by" back={d.bulk.back} />
              <span>Link selected issues to</span>
              <label><span className="sr">Problem</span><select name="problem" defaultValue=""><option value="">Choose a problem</option>{d.bulk.problems.map((o) => <option key={o.value} value={o.value}>{o.label}</option>)}</select></label>
              <label><span className="sr">or a P- number</span><input type="text" name="pnum" inputMode="numeric" maxLength={11} placeholder="or P-number" autoComplete="off" /></label>
              <Button variant="outline" size="sm">Link selected</Button>
              <Button type="reset" variant="ghost" size="sm">Clear</Button>
            </form>
          ) : null}
        </PlainCard>
      ) : null}
      {d.ready && d.view === "messages" ? (
        <PlainCard as="section" className="l-card" aria-label="Messages">
          {d.rows?.length ? (
            <div className="tablewrap" role="region" aria-label="Messages table" tabIndex={0}><table className="l-table q-t"><thead><tr><th scope="col">Time</th><th scope="col">Sender</th><th scope="col">Message</th><th scope="col">Argus&apos;s disposition</th><th scope="col">Actions taken</th></tr></thead>
              <tbody>{d.rows.map((m) => (
                <tr key={m.id}><td data-label="Time" className="nw"><When ts={m.at} />{m.reconstructed ? <>{" "}<Pill title="rebuilt from the stored fetch files and run summaries">reconstructed</Pill></> : null}</td>
                  <td data-label="Sender"><Sender m={m} /></td><td className="t-main"><MsgBody m={m} /><p className="muted small"><span dir="auto">{m.room}</span>{m.link ? <> · <a href={m.link} rel="noopener noreferrer">Open in Matrix</a></> : null}</p></td>
                  <td data-label="Disposition"><Disp m={m} /></td><td data-label="Actions"><Chain c={m.chain ?? []} /></td></tr>
              ))}</tbody></table></div>
          ) : d.list?.filtered ? <p className="empty pad">No message matches these filters. <a href={d.list.clearHref}>Clear the filters</a></p> : <p className="empty pad">No {d.room} message recorded yet. They appear after the next chat-triage run.</p>}
        </PlainCard>
      ) : null}
      {d.list ? <Pager list={d.list} /> : null}
      {d.notIssues ? (
        <PlainCard as="details" className="fold-t q-ni"><summary>{d.notIssues.count} {d.notIssues.count === 1 ? "message was" : "messages were"} not issues <span className="muted">{d.notIssues.note}</span></summary>
          <div className="tablewrap" role="region" aria-label="Not issues" tabIndex={0}><table className="l-table q-t"><thead><tr><th scope="col">Time</th><th scope="col">Sender</th><th scope="col">Message</th><th scope="col">Why not an issue</th></tr></thead>
            <tbody>{d.notIssues.rows.map((m) => <tr key={m.id}><td data-label="Time" className="nw"><When ts={m.at} /></td><td data-label="Sender"><Sender m={m} /></td><td className="t-main"><MsgBody m={m} /></td><td data-label="Why not an issue"><Disp m={m} /></td></tr>)}</tbody></table></div>
          <p className="muted small">{d.notIssues.count > 15 ? "The 15 newest. " : null}<a href="/qa?view=messages">All messages</a> lists every message with its disposition.</p>
        </PlainCard>
      ) : null}
      <p className="l-note">{d.view === "messages" ? "Message text is quoted as received (secrets redacted, capped at 4000 characters). The chain on the right follows what Argus did: triage run, problem, incident, RCA report, Jira task, fix run, Matrix thread." : `One row per problem reported in ${d.room ?? "the QA room"}, its messages folded inside. Actionable issues are investigated (incident, RCA, Jira); for the others Argus asks the reporter in their thread. Message text is quoted as received (secrets redacted).`}</p>
    </>
  );
}

import { HelpQ } from "@/components/HelpQ";
import { proposalAct, proposalSettings } from "./actions";
import "./queue.css";
import { AdminsOnly } from "@/components/Agent";
import "@/components/work.css";
import { Reload } from "@/components/Infra";
import { EnvPill, Pill } from "@/components/Pills";
import { Banners, Crumb, Hidden, Tiles, tone, When } from "@/components/Section";
import { apiGet, csrfToken, readFlash } from "@/lib/server";
import type { ProposalAccept, ProposalConfirm, ProposalRow, Proposals } from "@/lib/types";
import { Button, buttonVariants } from "@/components/ui/button";
import { PlainCard } from "@/components/ui/card";
import { GetForm, SoftLink } from "@/components/Soft";

const Sev = ({ s }: { s: ProposalRow["severity"] }) => (s ? <Pill tone={tone(s.kind)}>{s.text}</Pill> : null);

function Accept({ p, a, csrf, here }: { p: ProposalRow; a: ProposalAccept; csrf: string; here: string }) {
  const known = a.users.some((u) => u.value === p.suggested);
  return (
    <details className="fold"><summary>Accept &amp; create {a.noun}</summary>
      <form action={proposalAct} className="fl-form">
        <Hidden csrf={csrf} return={here} action="accept" id={p.id} edit="1" />
        {a.trackers ? <fieldset className="fx-hosts"><legend>Create in</legend>
          <label className="check"><input type="radio" name="tracker" value="jira" defaultChecked={a.tracker !== "gitlab"} /><span><b>Jira</b> <small className="muted">{a.trackers.jira}</small></span></label>
          <label className="check"><input type="radio" name="tracker" value="gitlab" defaultChecked={a.tracker === "gitlab"} /><span><b>GitLab issues</b> <small className="muted">{a.trackers.gitlab}</small></span></label>
          {a.trackers.projects.length ? <label className="field"><span>GitLab project</span><select name="gl_project" defaultValue={a.trackers.project}>{a.trackers.projects.map((o) => <option key={o.value} value={o.value}>{o.label}</option>)}</select></label> : null}
        </fieldset> : null}
        <label className="field"><span>Title</span><input name="title" defaultValue={p.title} maxLength={a.maxTitle} required dir="auto" /></label>
        <div className="fl-grid">
          <label className="field"><span>Assignee</span><select name="assignee" defaultValue={p.suggested}>{p.suggested && !known ? <option value={p.suggested}>{p.suggested} (suggested)</option> : null}{a.users.map((u) => <option key={u.value} value={u.value}>{u.label}</option>)}<option value="">Unassigned</option></select></label>
          <label className="field"><span>Project</span><select name="project" defaultValue={a.project}>{a.projects.length ? a.projects.map((x) => <option key={x}>{x}</option>) : <option value="">default</option>}</select></label>
          <label className="field"><span>Priority</span><select name="priority" defaultValue={p.priority}>{a.priorities.map((x) => <option key={x}>{x}</option>)}</select></label>
          <label className="field"><span>Labels</span><input name="labels" defaultValue={p.labels} maxLength={300} placeholder="space separated" /></label>
        </div>
        {!a.users.length ? <p className="muted small">The assignable users of this project are not loaded yet (the board sync reads them from Jira); leave Unassigned or keep the suggestion.</p> : null}
        <div className="fl-acts"><Button variant="outline" size="sm">{a.button}</Button><span className="muted small">{a.note}</span></div>
      </form></details>
  );
}

function Row({ p, d, csrf }: { p: ProposalRow; d: Proposals; csrf: string }) {
  return (
    <li id={`p-${p.id}`} className="fl-row">
      {d.bulk ? <label className="l-hit"><input className="l-pick" type="checkbox" name="id" value={p.id} form="pbulk" defaultChecked={d.picked} aria-label={`Select ${p.title}`} /></label> : null}
      <div className="fl-main">
        <div className="fl-t"><Sev s={p.severity} /><b dir="auto">{p.title}</b><When ts={p.at} /></div>
        <div className="fl-m"><span>{p.meta[0]}</span>{p.source.href ? <a href={p.source.href}>{p.source.text}</a> : <span>{p.source.text}</span>}{p.env ? <EnvPill env={p.env} /> : null}{p.meta.slice(1).map((m, n) => <span key={n} dir="auto">{m}</span>)}</div>
        {p.why ? <p className="small" dir="auto">Why: {p.why}</p> : null}
        <div className="fl-acts">
          <details className="fold"><summary>Details</summary>
            {p.description ? <p className="pre" dir="auto">{p.description}</p> : null}
            {p.evidence ? <><p className="muted small">Evidence, goes into the task as is</p><pre className="code prompt" role="region" aria-label="Evidence" tabIndex={0}><code>{p.evidence}</code></pre></> : null}
            {p.fixPrompt ? <><p className="muted small">fix-prompt.txt, goes into the task as is</p><pre className="code prompt" role="region" aria-label="Fix prompt" tabIndex={0}><code>{p.fixPrompt}</code></pre></> : null}
            <dl className="fl-kv">{p.facts.map((f) => <div key={f.label}><dt>{f.label}</dt><dd dir="auto">{f.value}</dd></div>)}</dl>
          </details>
          {d.accept ? <Accept p={p} a={d.accept} csrf={csrf} here={d.here} /> : null}
          {p.snooze === "ok" ? <form action={proposalAct}><Hidden csrf={csrf} return={d.here} action="snooze" id={p.id} /><Button variant="outline" size="sm" title="Back in the waiting list after 24 hours; the expiry still counts">Snooze 24 h</Button></form>
            : p.snooze === "expiring" ? <span className="muted small" title="Snoozing lasts 24 hours and does not extend the expiry">No snooze: it expires before 24 h are up</span> : null}
          <details className="fold"><summary>Reject</summary><form action={proposalAct} className="fl-in"><Hidden csrf={csrf} return={d.here} action="reject" id={p.id} />
            <label className="sr" htmlFor={`pr-${p.id}`}>Reason</label><input id={`pr-${p.id}`} type="text" name="reason" maxLength={d.maxReason} placeholder="Reason (optional, remembered)" /><Button variant="destructive" size="sm">Reject</Button></form></details>
        </div>
      </div>
    </li>
  );
}

function Confirm({ c, csrf, here }: { c: ProposalConfirm; csrf: string; here: string }) {
  if (c.stop) return <PlainCard as="section"><p className="empty">{c.stop} <a href={c.cancel}>Back to the list</a></p></PlainCard>;
  return (
    <PlainCard as="section" aria-labelledby="qc-h"><div className="card-h"><h2 id="qc-h">{c.head}</h2></div>
      {c.block ? <Banners list={[{ text: c.block, kind: "fail" }]} /> : null}
      {c.body.map((b, n) => <p key={n} className="card-sub">{b}</p>)}
      {c.warn ? <p className="r-denied small">{c.warn}</p> : null}
      <ul className="plain">{c.open.map((o, n) => <li key={n}><Sev s={o.severity} /> <span dir="auto">{o.title}</span>{o.env ? <> <EnvPill env={o.env} /></> : null}</li>)}</ul>
      {c.skipped ? <p className="muted small">{c.skipped} of the selected {c.skipped === 1 ? "is" : "are"} no longer waiting and will be skipped.</p> : null}
      <form action={proposalAct} className="fl-form"><Hidden csrf={csrf} return={here} action={c.action} />
        {c.ids.map((i) => <input key={i} type="hidden" name="id" value={i} />)}
        {c.reason ? <label className="field"><span>Reason (optional, remembered)</span><input name="reason" maxLength={c.reason} placeholder="For example: duplicate of OPS-211" /></label> : null}
        <div className="fl-acts"><Button variant={c.danger ? "destructive" : "default"} disabled={!!c.block}>{c.button}</Button><a data-slot="button" className={buttonVariants({ variant: "outline" })} href={c.cancel}>Cancel</a>
          <span className="muted small">The result shows at the top of the list when the board sync applies it, within seconds. Sending twice is safe: a proposal no longer waiting is skipped and counted.</span></div>
      </form>
    </PlainCard>
  );
}

/** Review's full list of proposed tasks: the views, filters, bulk confirm step, approval settings and the older tasks. */
export async function ProposalsFull({ query }: { query: string }) {
  const [r, csrf, flash] = await Promise.all([apiGet<Proposals>("/api/v1/proposals?" + query), csrfToken(), readFlash()]);
  if (r.status === 403) return <><header className="phead"><div><Crumb href="/review?kind=proposals" label="Review" /><h1>Proposed tasks</h1><HelpQ p="/review" /></div></header><AdminsOnly /></>;
  if (r.status !== 200) throw new Error(`/api/v1/proposals answered ${r.status}`);
  const d = r.data;
  const title = d.view === "pending" ? "Waiting for your OK" : d.view === "snoozed" ? "Snoozed" : "Decided in the last 14 days";
  return (
    <>
      <Reload r={d.refresh} />
      <header className="phead"><div><Crumb href="/review?kind=proposals" label="Review" /><h1>Proposed tasks</h1><HelpQ p="/review" />
        <p className="meta-line"><b>{d.pending ? `${d.pending} proposed task${d.pending === 1 ? "" : "s"} waiting for your OK` : "Nothing waiting"}</b>{d.autoOn.length ? <Pill tone="fail">auto-create ON: {d.autoOn.join(", ")}</Pill> : <span>· nothing reaches Jira without approval</span>}<span>· undecided proposals expire {d.expiryDays} d after they were proposed</span></p></div></header>
      <Banners list={d.banners} flash={flash} />
      <div className="stack">
        <Tiles tiles={d.tiles} />
        <p className="muted small">Rejected today: <a href="/review?kind=proposals&mode=full&view=decided">{d.rejectedToday}</a>, including expired · Created before approval: {d.legacy.length ? <a href="#legacy">{d.legacy.length}</a> : 0}, review only</p>
        <nav className="tabs" aria-label="Proposed tasks views">{d.tabs.map((t) => <SoftLink key={t.href} href={t.href} className={t.current ? "on" : undefined} aria-current={t.current ? "page" : undefined}>{t.label} <span className="ncount">{t.count}</span></SoftLink>)}</nav>
        {d.confirm ? <Confirm c={d.confirm} csrf={csrf} here={d.here} /> : <>
          {d.total > 0 ? (
            <section className="fl-tools" aria-label="Filter proposed tasks">
              <GetForm action="/review" role="search" className="fl-in">
                {d.search.map(([k, v], n) => <input key={n} type="hidden" name={k} value={v} />)}
                <label className="sr" htmlFor="pq">Search proposed tasks</label><input id="pq" type="text" name="q" defaultValue={d.q} maxLength={100} placeholder="Title, source, host, reason, labels" autoComplete="off" />
                {d.sorts.length ? <label className="f-pill"><span className="f-l">Sort</span><select name="sort" defaultValue={d.sort}>{d.sorts.map((o) => <option key={o.value} value={o.value}>{o.label}</option>)}</select></label> : null}
                <Button variant="outline" size="sm">Apply</Button>{d.filtered ? <a data-slot="button" className={buttonVariants({ variant: "ghost", size: "sm" })} href={d.clear}>Reset</a> : null}
              </GetForm>
              {d.chips.map((row) => <div key={row.label} className="fl-chips" role="group" aria-label={row.label}><span className="muted small">{row.label}</span>{row.chips.map((c) => <a key={c.label} className={c.on ? "chip on" : "chip"} href={c.href} aria-current={c.on ? "true" : undefined}>{c.label}{c.count >= 0 ? ` ${c.count}` : ""}</a>)}</div>)}
            </section>
          ) : null}
          <PlainCard as="section" className="l-card" aria-labelledby="pp-h"><div className="card-h pad"><h2 id="pp-h">{title} <span className="ncount">{d.matched}</span></h2>{d.rows.length && d.selHref ? <a className="small" href={d.selHref}>{d.picked ? "Clear selection" : `Select all ${d.rows.length} on this page`}</a> : null}</div>
            {d.view === "decided" ? (d.decided.length ? (
              <div className="tablewrap" role="region" aria-label="Decided proposals" tabIndex={0}><table className="l-table"><thead><tr><th scope="col">Decided</th><th scope="col">Proposal</th><th scope="col">Outcome</th><th scope="col">By</th><th scope="col">Reason</th><th scope="col">Task</th></tr></thead>
                <tbody>{d.decided.map((p) => <tr key={p.id}><td data-label="Decided" className="nw"><When ts={p.at} /></td><td className="t-main"><Sev s={p.severity} /> <span dir="auto">{p.title}</span><p className="muted small">{p.source.href ? <a href={p.source.href}>{p.source.text}</a> : p.source.text}</p></td>
                  <td data-label="Outcome"><Pill tone={tone(p.state.kind)}>{p.state.text}</Pill></td><td data-label="By" className="nw">{p.by}</td><td data-label="Reason" dir="auto">{p.reason}</td><td data-label="Task" className="nw">{p.task ? <a className="mono" href={p.task.href}>{p.task.text}</a> : <span className="muted">-</span>}</td></tr>)}</tbody></table></div>
            ) : <p className="empty pad">{d.total ? <>No decided task matches these filters. <a href={d.clear}>Clear the filters</a></> : "Nothing decided in the last 14 days. Accepted, rejected and expired proposed tasks stay here for 14 days."}</p>)
              : d.rows.length ? <ul className="rows">{d.rows.map((p) => <Row key={p.id} p={p} d={d} csrf={csrf} />)}</ul>
                : <p className="empty pad">{d.total ? <>No proposed task matches these filters. <a href={d.clear}>Clear the filters</a></> : d.view === "pending" ? "No proposed task is waiting. Argus proposes a task when an incident meets the QA intake threshold (or is in production), for page-level incidents when that option is on, and for security, CI/CD and credential findings." : "No proposed task is snoozed. A snoozed proposal comes back to Waiting after 24 hours."}</p>}
            {d.rows.length && d.bulk ? (
              <GetForm id="pbulk" action="/review" className="l-bulk" aria-label="Act on the selected proposed tasks" scroll>
                {d.bulk.map(([k, v], n) => <input key={n} type="hidden" name={k} value={v} />)}
                <span className="muted small">Selected</span><Button name="confirm" value="accept" variant="outline" size="sm">Accept &amp; create {d.bulkNoun}…</Button>
                {d.view !== "snoozed" ? <Button name="confirm" value="snooze" variant="outline" size="sm">Snooze 24 h…</Button> : null}
                <Button name="confirm" value="reject" variant="destructive" size="sm">Reject…</Button>
              </GetForm>
            ) : null}
            {d.pager && d.pager.pages > 1 ? <nav className="l-pager" aria-label="Pages"><span className="muted num">{d.pager.from}–{d.pager.to} of {d.pager.matched} · page {d.pager.page} of {d.pager.pages}</span>{d.pager.prev ? <a data-slot="button" className={buttonVariants({ variant: "outline", size: "sm" })} rel="prev" href={d.pager.prev}>Previous</a> : null}{d.pager.next ? <a data-slot="button" className={buttonVariants({ variant: "outline", size: "sm" })} rel="next" href={d.pager.next}>Next</a> : null}</nav> : null}
          </PlainCard>
        </>}
        {d.legacy.length ? (
          <PlainCard as="details" className="fold-t" id="legacy"><summary>Created automatically before approval existed <span className="muted">{d.legacy.length}</span></summary>
            <p className="muted small">Argus created these by itself before approval existed. They are listed for review only: nothing here closes, deletes or changes them in Jira.</p>
            <div className="tablewrap" role="region" aria-label="Older tasks" tabIndex={0}><table className="l-table"><thead><tr><th scope="col">Jira</th><th scope="col">Task</th><th scope="col">Jira status</th><th scope="col">Env</th><th scope="col">Created</th></tr></thead>
              <tbody>{d.legacy.map((t) => <tr key={t.href}><td data-label="Jira" className="nw mono">{t.key.href ? <a href={t.key.href} rel="noopener noreferrer">{t.key.text}</a> : t.key.text}</td><td className="t-main"><a href={t.href} dir="auto">{t.title}</a>{t.source ? <p className="muted small">{t.source.href ? <a href={t.source.href}>{t.source.text}</a> : t.source.text}</p> : null}</td>
                <td data-label="Jira status">{t.status}{t.gone ? <> <Pill>gone</Pill></> : null}</td><td data-label="Env">{t.env ? <EnvPill env={t.env} /> : null}</td><td data-label="Created" className="nw"><When ts={t.at} /></td></tr>)}</tbody></table></div></PlainCard>
        ) : null}
        <PlainCard as="details" className="fold-t"><summary>Approval settings <span className="muted">{d.settings.note}</span></summary>
          <p className="muted small">Off (the default) means every automatic path only proposes; nothing reaches Jira until an admin accepts it here. Turning a path on brings back the old behaviour for that path only: Argus creates the Jira issue by itself.</p>
          <form action={proposalSettings} className="fl-form"><Hidden csrf={csrf} return={d.here} />
            {d.settings.auto.map((a) => <label key={a.key} className="check"><input type="checkbox" name={`auto_${a.key}`} value="1" defaultChecked={a.on} /><span>{a.label}: create in Jira without approval</span></label>)}
            <div className="fl-grid">
              <label className="field"><span>Expire undecided proposals after (days)</span><input type="number" name="expiry_days" min={1} max={90} defaultValue={d.settings.expiryDays} /></label>
              <label className="field"><span>Propose a rejected source again after (days)</span><input type="number" name="reject_days" min={1} max={90} defaultValue={d.settings.rejectDays} /></label>
            </div>
            <div className="fl-acts"><Button variant="outline">Save</Button></div>
          </form></PlainCard>
      </div>
    </>
  );
}

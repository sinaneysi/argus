import { HelpQ } from "@/components/HelpQ";
import type { Metadata } from "next";
import { notFound } from "next/navigation";
import { Banner } from "@/components/Banner";
import { Icon } from "@/components/Icon";
import { EnvPill, Pill } from "@/components/Pills";
import { Banners, Crumb, FixCards, Hidden, Item, LinkRow, pick, Rail, Refresh, type Search, Suggestion, Summary, Timeline, tone } from "@/components/Section";
import { qs } from "@/lib/api";
import { full } from "@/lib/format";
import { apiGet, csrfToken, readFlash } from "@/lib/server";
import type { Problem } from "@/lib/types";
import { problemAct } from "../../incidents/actions";
import { Button } from "@/components/ui/button";
import { PlainCard } from "@/components/ui/card";

export const metadata: Metadata = { title: "Problem" };

const MORE = 15;

function Controls({ d, csrf }: { d: Problem; csrf: string }) {
  const c = d.controls;
  if (!c) return null;
  return (
    <PlainCard as="section" className="rail-c" aria-labelledby="pc-h">
      <h2 id="pc-h">Actions</h2>
      <div className="ctls">
        <details className="ctl"><summary>Change status</summary>
          <form action={problemAct} className="ctl-f"><Hidden csrf={csrf} action="status" problem={d.id} back={d.back} />
            <label className="field"><span>To</span><select name="to">{c.moves.map((o) => <option key={o.value} value={o.value}>{o.label}</option>)}</select></label>
            <label className="field"><span>Root cause</span><input name="root_cause" maxLength={400} defaultValue={d.rootCause} placeholder="Required for known error" /></label>
            <label className="field"><span>Proof</span><select name="root_cause_label" defaultValue=""><option value="">no change</option><option value="proven">proven</option><option value="probable">probable</option><option value="unproven">unproven</option></select></label>
            <label className="field"><span>Workaround</span><input name="workaround" maxLength={600} defaultValue={d.workaround} /></label>
            <label className="field"><span>Note</span><input name="note" maxLength={300} autoComplete="off" /></label>
            <Button variant="outline" size="sm">Change status</Button></form></details>
        {c.canResolve ? (
          <details className="ctl"><summary>Resolve…</summary>
            <form action={problemAct} className="ctl-f"><Hidden csrf={csrf} action="resolve" problem={d.id} back={d.back} />
              {c.issues.length ? (
                <fieldset className="picks"><legend>Also resolve these linked incidents</legend>
                  {c.issues.map((i) => <label key={i.id} className="check"><input type="checkbox" name="issue" value={i.id} defaultChecked={i.quiet} />#{i.id} <span dir="auto">{i.title}</span> <Pill tone={i.quiet ? "ok" : "fail"}>{i.state}</Pill></label>)}
                </fieldset>
              ) : <p className="muted small">No open incident is linked; QA issues follow their incident, CVE and CI/CD incidents resolve on evidence.</p>}
              <label className="field"><span>Note</span><input name="note" required minLength={3} maxLength={300} placeholder="The fix, and how it was verified" autoComplete="off" /></label>
              <Button variant="outline" size="sm">Resolve problem</Button></form></details>
        ) : null}
        <details className="ctl"><summary>Owner</summary>
          <form action={problemAct} className="ctl-f"><Hidden csrf={csrf} action="edit" problem={d.id} back={d.back} />
            <label className="field"><span>User</span><input name="owner" maxLength={40} defaultValue={d.owner} placeholder="user name, empty to clear" autoComplete="off" /></label>
            <Button variant="outline" size="sm">Save</Button></form></details>
        <details className="ctl"><summary>Merge into…</summary>
          <form action={problemAct} className="ctl-f"><Hidden csrf={csrf} action="merge" problem={d.id} back={d.back} />
            <label className="field"><span>P-number</span><input name="into" required inputMode="numeric" maxLength={11} placeholder="P-12" autoComplete="off" /></label>
            <Button variant="destructive" size="sm">Merge</Button>
            <p className="muted small">This problem is closed as a duplicate; its links move to the other one.</p></form></details>
      </div>
    </PlainCard>
  );
}

export default async function ProblemPage({ searchParams }: { searchParams: Search }) {
  const params = await pick(searchParams, ["id", "sent", "preq"]);
  const [r, csrf, flash] = await Promise.all([apiGet<Problem>("/api/v1/problems/item" + qs(params)), csrfToken(), readFlash()]);
  if (r.status === 404) notFound();
  if (r.status !== 200) throw new Error(`/api/v1/problems/item answered ${r.status}`);
  const d = r.data;
  return (
    <>
      <Refresh on={d.refresh} />
      <header className="phead"><div><Crumb href="/problems" label="Problems" /><h1 dir="auto">{d.title}</h1><HelpQ p="/problems" />
        <p className="meta-line"><Pill tone={tone(d.statusKind)}>{d.statusWord}</Pill>{d.rootLabel ? <Pill>{d.rootLabel}</Pill> : null}<span className="mono">{d.label}</span>{d.env ? <EnvPill env={d.env} /> : null}<span>{d.explains}</span>
          {d.fixSince ? <span>fix in progress since {full(d.fixSince)}</span> : null}</p></div></header>
      <Banners list={d.banners} flash={flash} />
      {d.mergedInto ? <p className="banner warn" role="status"><Icon name="warn" /><span>Closed as a duplicate: this problem was merged into <a href={`/problems/view?id=${d.mergedInto}`}><b>P-{d.mergedInto}</b></a>. Its links live there now.</span></p> : null}
      {d.prod ? <Banner b={{ kind: "info", text: "Production: every fix step needs the operator's explicit approval. Argus never changes production." }} /> : null}
      <Summary l={d.lazy} />
      <div className="detail">
        <div className="stack">
          <PlainCard as="section" className="l-card" id="linked" aria-labelledby="lk-h">
            <div className="card-h pad"><h2 id="lk-h">Linked items <span className="ncount">{d.linkedCount}</span></h2></div>
            {d.groups.map((g) => (
              <div key={g.key}>
                <h3 className="grp">{g.title} <span className="ncount">{g.links.length}</span></h3>
                <ul className="rows">{g.links.slice(0, MORE).map((l) => <LinkRow key={l.id} l={l} back={d.back} csrf={csrf} />)}</ul>
                {g.links.length > MORE ? <details className="fold pad"><summary>More <span className="muted">{g.links.length - MORE}</span></summary><ul className="rows">{g.links.slice(MORE).map((l) => <LinkRow key={l.id} l={l} back={d.back} csrf={csrf} />)}</ul></details> : null}
              </div>
            ))}
            {d.incoming.length ? (
              <div><h3 className="grp">Other problems pointing here <span className="ncount">{d.incoming.length}</span></h3>
                <ul className="rows">{d.incoming.map((x) => <li key={x.id}><p><a href={`/problems/view?id=${x.id}`}><b>{x.label}</b></a> <span dir="auto">{x.title}</span> <span className="muted">{x.word} this problem</span></p></li>)}</ul></div>
            ) : null}
            {!d.groups.length && !d.incoming.length ? <p className="empty"><b>Nothing linked yet.</b> Open an incident, QA issue, CVE group, CI/CD finding or RCA and choose &quot;Link to problem…&quot;, or select incidents on the <a href="/incidents">Incidents</a> list and link them in bulk.</p> : null}
          </PlainCard>
          {d.suggestions.length ? (
            <PlainCard as="section" id="suggested" aria-labelledby="sg-h">
              <div className="card-h"><h2 id="sg-h">Suggested <span className="ncount">{d.suggestions.length}</span></h2><a className="small" href={`/review?kind=links&p=${d.id}`}>Decide in bulk</a></div>
              <ul className="sugs">{d.suggestions.map((s) => <Suggestion key={s.id} s={s} back={d.back + "#suggested"} csrf={csrf} />)}</ul>
            </PlainCard>
          ) : null}
          {d.gone.length ? (
            <PlainCard as="details" className="fold"><summary>Removed and rejected links <span className="muted">{d.gone.length}</span></summary>
              <p className="card-sub">Kept so a rejected pair is not suggested again.</p>
              <ul className="plain">{d.gone.map((l) => <li key={l.id}><Item it={l.item} /> <Pill>{l.state}</Pill>{l.why ? <span className="muted small"> {l.why}</span> : null}</li>)}</ul></PlainCard>
          ) : null}
          <Timeline list={d.timeline} note="This problem's own events and those of its linked incidents, RCAs, QA issues, CVE groups and CI/CD findings, newest first." />
          {d.fix ? <FixCards f={d.fix} /> : null}
        </div>
        <aside><Rail facts={d.facts} /><Controls d={d} csrf={csrf} /></aside>
      </div>
    </>
  );
}

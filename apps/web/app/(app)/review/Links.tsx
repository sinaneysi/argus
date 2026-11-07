import { HelpQ } from "@/components/HelpQ";
import "./queue.css";
import { redirect } from "next/navigation";
import { problemAct } from "@/app/(app)/incidents/actions";
import { Reload } from "@/components/Infra";
import { Banners, Crumb, Filters, Hidden, Item, Pager, When } from "@/components/Section";
import { apiGet, csrfToken, readFlash } from "@/lib/server";
import type { LinkConfirm, Links, ProbSuggestion } from "@/lib/types";
import { Button, buttonVariants } from "@/components/ui/button";
import { PlainCard } from "@/components/ui/card";

function Row({ s, d, csrf }: { s: ProbSuggestion; d: Links; csrf: string }) {
  return (
    <li id={`s-${s.id}`} className="fl-row">
      {d.can ? <label className="l-hit"><input className="l-pick" type="checkbox" name="link" value={s.id} form="pbsug" defaultChecked={d.picked} aria-label={`Select ${s.item?.label ?? s.id} to ${s.problem?.label ?? "problem"}`} /></label> : null}
      <div className="fl-main">
        <div className="fl-t"><b className="num">{s.confidence}</b><span>{s.item ? <Item it={s.item} /> : null} → {s.problem ? <><a href={`/problems/view?id=${s.problem.id}`}><b>{s.problem.label}</b></a> <span dir="auto">{s.problem.title}</span></> : null}</span><When ts={s.at} /></div>
        <div className="fl-m"><span>{s.linkWord}</span><span>suggested by {s.from}</span></div>
        {s.reason ? <p className="small" dir="auto">Why: {s.reason}</p> : null}
        {s.canDecide ? (
          <div className="fl-acts">
            <form action={problemAct}><Hidden csrf={csrf} action="accept" link={s.id} back={d.here ?? ""} /><Button variant="outline" size="sm" title={`Links the item to the problem as ${s.linkWord}`}>Accept link</Button></form>
            <details className="fold"><summary>Reject</summary><form action={problemAct} className="fl-in"><Hidden csrf={csrf} action="reject" link={s.id} back={d.here ?? ""} />
              <label className="sr" htmlFor={`sr-${s.id}`}>Reason</label><input id={`sr-${s.id}`} type="text" name="note" maxLength={d.maxNote} placeholder="Reason (optional, remembered)" /><Button variant="destructive" size="sm">Reject</Button></form></details>
          </div>
        ) : null}
      </div>
    </li>
  );
}

function Confirm({ c, csrf }: { c: LinkConfirm; csrf: string }) {
  if (c.error) return <PlainCard as="section"><p className="empty"><b>{c.error}</b> {c.note}</p><p className="row-act"><a data-slot="button" className={buttonVariants({ variant: "outline" })} href={c.back}>Cancel</a></p></PlainCard>;
  return (
    <PlainCard as="section" aria-labelledby="qc-h"><div className="card-h"><h2 id="qc-h">{c.head}</h2></div>
      {c.body?.map((b, n) => <p key={n} className="card-sub">{b}</p>)}
      <ul className="plain">{c.items?.map((o, n) => <li key={n}><b className="num">{o.confidence}</b> {o.item} <span className="muted">{o.linkWord}</span> {o.problem} <span dir="auto">{o.title}</span></li>)}</ul>
      {c.skipped ? <p className="muted small">{c.skipped} of the selected {c.skipped === 1 ? "is" : "are"} no longer waiting and will be skipped.</p> : null}
      {c.gone?.length ? <ul className="plain muted small">{c.gone.map((g, n) => <li key={n}>{g.item} → {g.problem} · {g.state}</li>)}</ul> : null}
      <form action={problemAct} className="fl-form"><Hidden csrf={csrf} action={c.action ?? ""} back={c.back} />
        {c.links?.map((i) => <input key={i} type="hidden" name="link" value={i} />)}
        {c.action === "reject" ? <label className="field"><span>Reason (optional, remembered)</span><input name="note" maxLength={c.maxNote} placeholder="For example: different upstream" /></label> : null}
        <div className="fl-acts"><Button variant={c.action === "reject" ? "destructive" : "default"}>{c.button}</Button><a data-slot="button" className={buttonVariants({ variant: "outline" })} href={c.back}>Cancel</a>
          <span className="muted small">The result shows at the top of the list when Argus applies it, within seconds. Sending twice is safe: a link no longer waiting is skipped.</span></div>
      </form>
    </PlainCard>
  );
}

/** Review's full list of suggested problem links: filters, one-by-one decisions and the bulk confirm step. */
export async function LinksFull({ query }: { query: string }) {
  const [r, csrf, flash] = await Promise.all([apiGet<Links>("/api/v1/suggestions?" + query), csrfToken(), readFlash()]);
  if (r.status === 404) redirect("/review");
  if (r.status !== 200) throw new Error(`/api/v1/suggestions answered ${r.status}`);
  const d = r.data;
  return (
    <>
      <Reload r={d.refresh} />
      <header className="phead"><div><Crumb href="/review?kind=links" label="Review" /><h1>Suggestions</h1><HelpQ p="/review" /><p className="meta-line">{d.line.map((l, n) => <span key={n}>{n ? " · " : ""}{n ? l : <b>{l}</b>}</span>)}</p></div></header>
      <Banners list={d.banners} flash={flash} />
      <div className="stack">
        {d.confirm ? <Confirm c={d.confirm} csrf={csrf} /> : (
          <>
            {d.any && d.list ? <Filters list={d.list} facets={d.facets ?? []} label="Filter suggestions" placeholder="P-12, problem title, item, reason" /> : null}
            {!d.any ? <PlainCard as="section"><p className="empty"><b>Nothing to decide.</b> Argus suggests links after each incident, RCA, CVE and CI/CD cycle and once a night, from shared fingerprints, upstreams, hosts, domains, components and RCA text. Suggestions below the threshold ({d.minConfidence}) are never shown.</p></PlainCard> : (
              <PlainCard as="section" className="l-card" aria-labelledby="ps-h"><div className="card-h pad"><h2 id="ps-h">Suggested links <span className="ncount">{d.list?.matched}</span></h2>
                {d.can && d.rows?.length ? <a className="small" href={d.selHref}>{d.picked ? "Clear selection" : `Select all ${d.rows.length} on this page`}</a> : null}</div>
                {d.problem ? <p className="card-sub pad">For <a href={`/problems/view?id=${d.problem.id}`}>{d.problem.label}</a> · <a href="/review?kind=links&mode=full">all problems</a></p> : null}
                {d.rows?.length ? <ul className="rows">{d.rows.map((s) => <Row key={s.id} s={s} d={d} csrf={csrf} />)}</ul>
                  : <p className="empty pad">No suggestion matches these filters. <a href={d.list?.clearHref}>Clear the filters</a></p>}
                {d.can && d.rows?.length ? (
                  <form id="pbsug" method="get" action="/review" className="l-bulk" aria-label="Act on the selected suggestions">
                    {d.bulk?.map(([k, v], n) => <input key={n} type="hidden" name={k} value={v} />)}
                    <span className="muted small">Selected</span><Button name="confirm" value="accept" variant="outline" size="sm">Accept links…</Button><Button name="confirm" value="reject" variant="destructive" size="sm">Reject…</Button>
                  </form>
                ) : null}
                {d.list ? <Pager list={d.list} /> : null}
              </PlainCard>
            )}
          </>
        )}
        <p className="muted small">A suggestion is never a decision: accepting links the item to the problem (banner, one Lazy reading, one thread); rejecting remembers the pair for {d.rejectDays} days. Confidence is the noisy-or of the matched features; the reason names the strongest two.</p>
      </div>
    </>
  );
}

import { HelpQ } from "@/components/HelpQ";
import type { Metadata } from "next";
import { scanAct } from "../inventory/actions";
import { P, Reload, Scan, Strip } from "@/components/Infra";
import { Banners, Hidden } from "@/components/Section";
import { apiGet, csrfToken, readFlash } from "@/lib/server";
import type { Checks, SelfRow } from "@/lib/types";
import { Button } from "@/components/ui/button";
import { PlainCard } from "@/components/ui/card";

export const metadata: Metadata = { title: "Checks" };

function SelfTable({ rows }: { rows: SelfRow[] }) {
  return (
    <div className="tablewrap" role="region" aria-label="Argus parts" tabIndex={0}><table className="l-table">
      <thead><tr><th scope="col">Status</th><th scope="col">Part</th><th scope="col">Group</th><th scope="col">Detail</th></tr></thead>
      <tbody>{rows.map((m, i) => <tr key={i}><td><P p={m.status} /></td><td className="t-main">{m.name}</td><td data-label="Group" className="muted">{m.group}</td><td data-label="Detail">{m.detail}</td></tr>)}</tbody>
    </table></div>
  );
}

export default async function ChecksPage() {
  const [r, csrf, flash] = await Promise.all([apiGet<Checks>("/api/v1/checks"), csrfToken(), readFlash()]);
  if (r.status !== 200) throw new Error(`/api/v1/checks answered ${r.status}`);
  const d = r.data;
  const x = d.exceptions;
  const bad = d.bad ?? [];
  return (
    <>
      <Reload r={d.refresh} />
      <header className="phead"><div><h1>Checks</h1><HelpQ p="/operations" />
        <p className="status-line"><b>{d.ok} of {d.total} checks OK</b>{bad.length ? <><span className="sep">·</span>{bad.join(", ")} need a look</> : null}{d.failing ? <><span className="sep">·</span>{d.failing} backup set{d.failing === 1 ? "" : "s"} failing</> : null}</p></div>
        <div className="phead-act"><Scan s={d.scan} csrf={csrf} /></div></header>
      <Banners list={d.banners} flash={flash} />
      <PlainCard as="section" className="l-card" aria-labelledby="ka-h"><div className="card-h rows-h"><h2 id="ka-h">Needs attention <span className="ncount">{x.total}</span></h2></div>
        {x.rows.length ? <ul className="rows">{x.rows.map((e, i) => <li key={i}><p className="th"><P p={e.pill} /><a href={e.href}>{e.title}</a></p><p className="muted small">{e.kind} · {e.meta}</p></li>)}</ul>
          : <p className="empty">Nothing needs you: no failing backup set, no certificate within 30 days.</p>}
        {x.total > x.rows.length ? <p className="pad muted small card-gap">Showing {x.rows.length} of {x.total}</p> : null}
      </PlainCard>
      <PlainCard as="section" className="l-card" id="checks" aria-labelledby="kc-h"><div className="card-h rows-h"><h2 id="kc-h">Scheduled checks</h2></div>
        <ul className="rows">{d.checks.map((c) => (
          <li key={c.task}>
            <div className="card-h"><p className="th"><P p={c.status} /><a href={c.href}>{c.name}</a><span className="muted small">{c.last}</span></p>
              {c.busy ? <Button variant="outline" size="sm" disabled title={c.state}>Scanning…</Button> : <form action={scanAct}><Hidden csrf={csrf} tasks={c.task} back="/operations" /><Button variant="outline" size="sm" title={c.title}>Scan</Button></form>}</div>
            <p className="small" dir="auto">{c.summary}</p>
            <Strip s={c.strip} />
          </li>
        ))}</ul>
        <p className="pad muted small card-gap">Agent cost, 30 days: <b>{d.cost}</b> over {d.runs} runs · <a href="/runs">Runs</a></p>
      </PlainCard>
      {d.credentials ? (
        <PlainCard as="section" className="l-card" aria-labelledby="kr-h"><div className="card-h rows-h"><h2 id="kr-h">{d.credentials.title}</h2>{d.credentials.href ? <a className="small" href={d.credentials.href}>Credentials</a> : null}</div>
          <ul className="rows">
            {d.credentials.rows.map((c) => <li key={c.name}><div className="card-h"><p className="th">{c.href ? <a href={c.href}>{c.name}</a> : <b>{c.name}</b>}</p><span className="small muted">{c.expires}</span></div><p className="muted small">{c.detail}</p></li>)}
            {d.credentials.more ? <li><p className="muted small">{d.credentials.more} more on the Credentials page</p></li> : null}
            {d.credentials.line ? <li><p className="muted small">{d.credentials.line}</p></li> : null}
          </ul></PlainCard>
      ) : null}
      {d.readiness ? (
        <PlainCard as="section" className="l-card" aria-labelledby="ky-h"><div className="card-h rows-h"><h2 id="ky-h">{d.readiness.title}</h2></div>
          <p className="pad muted small">{d.readiness.date} · {d.readiness.when} · read-only report: release deploys, capacity headroom, edge 429s, landing pages, redis-admin memory.</p>
          {d.readiness.rows.length ? <div className="tablewrap" role="region" aria-label="Readiness" tabIndex={0}><table className="l-table"><thead><tr><th scope="col">Status</th><th scope="col">Check</th><th scope="col">Detail</th></tr></thead>
            <tbody>{d.readiness.rows.map((c) => <tr key={c.check}><td><P p={c.status} /></td><td className="t-main nw">{c.check}</td><td data-label="Detail">{c.detail}</td></tr>)}</tbody></table></div> : <p className="empty">Nothing collected yet.</p>}
          {d.readiness.gaps?.length ? <p className="pad muted small card-gap">Collection gaps: {d.readiness.gaps.join("; ")}</p> : null}
        </PlainCard>
      ) : null}
      <PlainCard as="section" className="l-card" aria-labelledby="kh-h"><div className="card-h rows-h"><h2 id="kh-h">Argus health <P p={d.selfcheck.status} /></h2></div>
        <p className="pad muted small">{d.selfcheck.line}</p>
        {d.selfcheck.bad.length ? <SelfTable rows={d.selfcheck.bad} /> : null}
        {d.selfcheck.ok.length ? <details className="fold-t fold-c"><summary>{d.selfcheck.ok.length} part{d.selfcheck.ok.length === 1 ? "" : "s"} OK</summary><SelfTable rows={d.selfcheck.ok} /></details> : null}
        {!d.selfcheck.bad.length && !d.selfcheck.ok.length ? <p className="empty">No checks recorded.</p> : null}
        <p className="pad muted small card-gap">Checked {d.selfcheck.at}{d.selfcheck.kuma ? ` · Kuma push: ${d.selfcheck.kuma}` : ""} · Argus-internal: these are Argus&apos;s own parts, not production.</p>
      </PlainCard>
    </>
  );
}

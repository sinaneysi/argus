import { HelpQ } from "@/components/HelpQ";
import type { Metadata } from "next";
import { redirect } from "next/navigation";
import { EnvPill, Pill } from "@/components/Pills";
import { Banners, Filters, Hidden, Pager, pick, Refresh, type Search, tone } from "@/components/Section";
import { qs } from "@/lib/api";
import { plural } from "@/lib/format";
import { apiGet, csrfToken, readFlash } from "@/lib/server";
import type { Problems } from "@/lib/types";
import { problemAct } from "../incidents/actions";
import { Button } from "@/components/ui/button";
import { PlainCard } from "@/components/ui/card";

export const metadata: Metadata = { title: "Problems" };

export default async function ProblemsPage({ searchParams }: { searchParams: Search }) {
  const params = await pick(searchParams, ["q", "status", "sev", "env", "sort", "page", "sent", "preq", "view", "p"]);
  if (params.view === "suggestions") redirect("/review" + qs({ kind: "links", p: params.p })); // the old URL: suggestions live in Review
  const [r, csrf, flash] = await Promise.all([apiGet<Problems>("/api/v1/problems" + qs(params)), csrfToken(), readFlash()]);
  if (r.status === 404) redirect("/today");
  if (r.status !== 200) throw new Error(`/api/v1/problems answered ${r.status}`);
  const d = r.data;
  return (
    <>
      <Refresh on={d.refresh} />
      <header className="phead"><div><h1>Problems</h1><HelpQ p="/problems" />
        <p className="status-line">
          <b>{d.open ? `${plural(d.open, "open problem")} explain ${plural(d.issues, "linked issue")}` : "No open problem"}</b><span className="sep">·</span>
          {d.suggested ? <a href="/review?kind=links">{plural(d.suggested, "suggested link")} waiting</a> : "no suggestion waiting"}<span className="sep">·</span>
          {d.resolved7} resolved in the last 7 days<span className="sep">·</span>{d.autoLine}
        </p></div></header>
      <Banners list={d.banners} flash={flash} />
      {d.canCreate ? (
        <details className="ctl new-p"><summary>Create problem</summary>
          <form action={problemAct} className="ctl-f"><Hidden csrf={csrf} action="create" back="/problems" />
            <label className="field"><span>Title</span><input name="title" required minLength={3} maxLength={200} placeholder="The root cause, in a few words" autoComplete="off" /></label>
            <label className="field"><span>Note</span><input name="note" maxLength={300} placeholder="Optional" autoComplete="off" /></label>
            <Button variant="outline" size="sm">Create problem</Button>
            <p className="muted small">Then link its incidents, QA issues and RCAs from their pages, or in bulk from the Incidents list.</p>
          </form></details>
      ) : null}
      {d.hasAny || d.suggested ? (
        <nav className="tabs" aria-label="Problem views">
          {d.tabs.map((t) => <a key={t.key} href={t.href} className={t.on ? "on" : undefined} aria-current={t.on ? "page" : undefined}>{t.label} <span className="num">{t.count}</span></a>)}
        </nav>
      ) : null}
      {d.hasAny ? <Filters list={d.list} facets={d.facets} label="Search and filter problems" placeholder="P-12, title, root cause, owner" /> : null}
      <PlainCard as="section" className="l-card" aria-labelledby="pl-h">
        <div className="card-h pad"><h2 id="pl-h">{d.heading} <span className="ncount">{d.list.matched}</span></h2></div>
        {d.rows.length ? (
          <ul className="rows">
            {d.rows.map((p) => (
              <li key={p.id} id={`p-${p.id}`}>
                <p className="th"><span className="mono">{p.label}</span><Pill tone={tone(p.statusKind)}>{p.statusWord}</Pill><a href={`/problems/view?id=${p.id}`} dir="auto">{p.title}</a></p>
                <p className="meta-line"><span>{p.linked}</span>{p.env ? <EnvPill env={p.env} /> : null}{p.severity ? <span>{p.severity}</span> : null}{p.public ? <Pill>customer-facing</Pill> : null}<span>{p.age}</span>{p.owner ? <span>owner {p.owner}</span> : null}</p>
                {p.rootCause ? <p className="small" dir="auto">Root cause: {p.rootCause}{p.rootLabel ? <span className="muted"> ({p.rootLabel})</span> : null}</p>
                  : p.mergedInto ? <p className="small">Merged into <a href={`/problems/view?id=${p.mergedInto}`}>P-{p.mergedInto}</a></p> : null}
              </li>
            ))}
          </ul>
        ) : !d.hasAny ? <p className="empty"><b>No problems yet.</b> A problem groups the incidents, QA issues, CVE groups and CI/CD findings that share one root cause. Create one from an RCA report or an incident, or accept one of Argus&apos;s suggestions. <a href="/rca">Open the RCA library</a></p>
          : d.list.filtered ? <p className="empty">No problem matches these filters. <a href={d.list.clearHref}>Clear the filters</a></p>
            : <p className="empty">Nothing here. <a href="/problems?status=all">Show all problems</a></p>}
      </PlainCard>
      <Pager list={d.list} />
      {d.backfill ? (
        <PlainCard as="details" className="fold new-b" id="backfill"><summary>Backfill plan (not applied) <span className="muted">{d.backfill.rows.length}</span></summary>
          {d.backfill.notes.map((n, i) => <p key={i} className="card-sub">{n}</p>)}
          <div className="tablewrap"><table className="l-table"><thead><tr><th>Action</th><th>Problem</th><th>Status</th><th>Incident</th><th>RCAs</th></tr></thead>
            <tbody>{d.backfill.rows.map((row, i) => <tr key={i}>{row.map((c, n) => <td key={n}>{c.href ? <a href={c.href}>{c.text}</a> : c.text}</td>)}</tr>)}</tbody></table></div>
        </PlainCard>
      ) : null}
    </>
  );
}

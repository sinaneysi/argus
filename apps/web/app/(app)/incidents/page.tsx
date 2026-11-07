import { HelpQ } from "@/components/HelpQ";
import type { Metadata } from "next";
import { EnvPill, Pill } from "@/components/Pills";
import { SevPill } from "@/components/Severity";
import { Button } from "@/components/ui/button";
import { Card, CardContent } from "@/components/ui/card";
import { Banners, Filters, Hidden, Pager, pick, Refresh, type Search, Tiles, tone, When } from "@/components/Section";
import { qs } from "@/lib/api";
import { day } from "@/lib/format";
import { apiGet, csrfToken, readFlash } from "@/lib/server";
import type { Incidents } from "@/lib/types";
import { problemAct } from "./actions";

export const metadata: Metadata = { title: "Incidents" };

export default async function IncidentsPage({ searchParams }: { searchParams: Search }) {
  const params = await pick(searchParams, ["q", "status", "sev", "env", "source", "sort", "page", "sent", "preq"]);
  const [r, csrf, flash] = await Promise.all([apiGet<Incidents>("/api/v1/incidents" + qs(params)), csrfToken(), readFlash()]);
  if (r.status !== 200) throw new Error(`/api/v1/incidents answered ${r.status}`);
  const d = r.data;
  const head = <header className="phead"><div><h1>Incidents</h1><HelpQ p="/incidents" /><p className="psub">What is broken, and why? One record per problem, from alerts, chat reports and log RCA.</p></div></header>;
  if (d.empty) return <>{head}<Card as="section"><CardContent><p className="empty"><b>No incident records yet.</b> {d.emptyText}</p></CardContent></Card></>;
  return (
    <>
      <Refresh on={d.refresh} />
      {head}
      <Banners list={d.banners} flash={flash} />
      <Tiles tiles={d.tiles} compact />
      <Filters list={d.list} facets={d.facets} label="Search and filter incidents" placeholder="Number, title, host or domain, key, owner" />
      <Card as="section" className="l-card block" aria-label="Incidents">
        {d.rows.length ? (
          <div className="tablewrap" role="region" aria-label="Incidents table" tabIndex={0}><table className="l-table tight">
            <thead><tr><th scope="col" className="num">#</th><th scope="col">Severity</th><th scope="col">Status</th><th scope="col">Env</th><th scope="col">Incident</th><th scope="col">Source</th><th scope="col" className="num" title="Findings attached">Findings</th><th scope="col" className="lo">First seen</th><th scope="col">Last finding</th><th scope="col">Owner</th></tr></thead>
            <tbody>
              {d.rows.map((x) => (
                <tr key={x.id}>
                  <td data-label="Number" className="nw num">{d.bulk ? <label className="l-hit"><input className="l-pick" type="checkbox" name="ref" value={`incident:${x.id}`} form="pblink" aria-label={`Select incident #${x.id}`} /></label> : null}#{x.id}</td>
                  <td data-label="Severity"><SevPill sev={x.sevKey} /></td>
                  <td data-label="Status"><Pill tone={tone(x.statusKind)}>{x.status}</Pill></td>
                  <td data-label="Env">{x.env ? <EnvPill env={x.env} /> : <span className="muted">-</span>}</td>
                  <td className="t-main">
                    <a href={`/incidents/view?id=${x.id}`} dir="auto">{x.title}</a>
                    {x.public ? <> <Pill tone="neutral" title="Customer-facing">public</Pill></> : null}
                    {x.hub ? <> <Pill tone="neutral" title={x.hub}>hub</Pill></> : null}
                    {x.problem ? <> <a className="kind" href={`/problems/view?id=${x.problem.id}`} title={x.problem.title}>{x.problem.label}</a></> : null}
                    <span className="sub">{[x.subject, x.family, x.dc].filter(Boolean).join(" · ")}</span>
                  </td>
                  <td data-label="Source" className="nw">{x.sources.map((s) => s.label).join(", ")}</td>
                  <td data-label="Findings" className="num">×{x.occurrences}</td>
                  <td data-label="First seen" className="lo nw muted">{day(x.firstSeen)}</td>
                  <td data-label="Last finding" className="nw muted" title={x.resolved ? "Resolved" : "Last finding"}><When ts={x.last} /></td>
                  <td data-label="Owner" className="nw">{x.owner || <span className="muted">-</span>}</td>
                </tr>
              ))}
            </tbody>
          </table></div>
        ) : d.list.filtered ? <p className="empty">No incident matches these filters. <a href={d.list.clearHref}>Clear the filters</a></p>
          : <p className="empty">No {d.list.noun}. New findings open an incident; the list fills itself.</p>}
        {d.bulk ? (
        <form id="pblink" action={problemAct} className="l-bulk">
          <Hidden csrf={csrf} action="link" link_type="caused_by" back={d.bulk.back} />
          <span>Link selected incidents to</span>
          <label><span className="sr">Problem</span><select name="problem" defaultValue=""><option value="">Choose a problem</option>{d.bulk.problems.map((o) => <option key={o.value} value={o.value}>{o.label}</option>)}</select></label>
          <label><span className="sr">or a P- number</span><input type="text" name="pnum" inputMode="numeric" maxLength={11} placeholder="or P-number" autoComplete="off" /></label>
          <Button variant="outline" size="sm">Link selected</Button>
          <Button type="reset" variant="ghost" size="sm">Clear</Button>
        </form>
      ) : null}
      </Card>
      <Pager list={d.list} />
      <p className="l-note">Auto-resolved when no matching alert fires for 6 h (chat reports: no new report for 24 h). Re-opened when the same problem comes back within 7 days. All single findings: <a href="/incidents/problems">{d.findingsWord} and RCA reports</a>. Every {d.room} message and what Argus did with it: <a href="/qa">Conversations</a>.</p>
    </>
  );
}

import { HelpQ } from "@/components/HelpQ";
import type { Metadata } from "next";
import { Pill } from "@/components/Pills";
import { Filters, Pager, pick, type Search, tone } from "@/components/Section";
import { qs } from "@/lib/api";
import { day } from "@/lib/format";
import { apiGet } from "@/lib/server";
import type { RcaList } from "@/lib/types";
import { PlainCard } from "@/components/ui/card";

export const metadata: Metadata = { title: "RCA library" };

export default async function RcaPage({ searchParams }: { searchParams: Search }) {
  const params = await pick(searchParams, ["q", "status", "source", "sort", "page"]);
  const r = await apiGet<RcaList>("/api/v1/rca" + qs(params));
  if (r.status !== 200) throw new Error(`/api/v1/rca answered ${r.status}`);
  const d = r.data;
  return (
    <>
      <header className="phead"><div><h1>RCA library</h1><HelpQ p="/rca" />
        <p className="status-line"><b>{d.count} reports</b><span className="sep">·</span>{d.notMerged} not merged yet</p>
        <p className="psub">Every root-cause analysis Argus wrote and the merged reports in the repository. A report shows here as soon as the run ends. Start a deep RCA from an incident page.</p></div></header>
      {d.count ? <Filters list={d.list} facets={d.facets} label="Search and filter reports" placeholder="Title, file, source or #incident" /> : null}
      <PlainCard as="section" className="l-card" aria-label="Reports">
        {d.rows.length ? (
          <div className="tablewrap" role="region" aria-label="Reports table" tabIndex={0}><table className="l-table">
            <thead><tr><th scope="col">Written</th><th scope="col">Report</th><th scope="col">Incident</th><th scope="col">MR state</th><th scope="col"><span className="sr">Merge request</span></th><th scope="col">Source</th><th scope="col">Cost</th></tr></thead>
            <tbody>{d.rows.map((x) => (
              <tr key={x.href}>
                <td data-label="Written" className="nw muted">{day(x.at)}</td>
                <td className="t-main"><a href={x.href} dir="auto">{x.title}</a><span className="sub mono">{x.file}</span></td>
                <td data-label="Incident">{x.incident ? <a href={`/incidents/view?id=${x.incident}`}>#{x.incident}</a> : <span className="muted">-</span>}</td>
                <td data-label="MR state"><Pill tone={tone(x.statusKind)}>{x.status}</Pill></td>
                <td data-label="Review">{x.mr ? <a href={x.mr} rel="noopener noreferrer">MR</a> : null}</td>
                <td data-label="Source" className="nw muted">{x.source}{x.auto ? " · auto" : ""}</td>
                <td data-label="Cost" className="nw muted">{x.cost}</td>
              </tr>
            ))}</tbody>
          </table></div>
        ) : d.count ? <p className="empty">No report matches these filters. <a href={d.list.clearHref}>Clear the filters</a></p> : <p className="empty">No RCA report yet.</p>}
      </PlainCard>
      <Pager list={d.list} />
    </>
  );
}

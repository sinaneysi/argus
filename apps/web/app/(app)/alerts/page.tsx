import { HelpQ } from "@/components/HelpQ";
import type { Metadata } from "next";
import { Banner } from "@/components/Banner";
import { Icon } from "@/components/Icon";
import { Pill } from "@/components/Pills";
import { Filters, Pager, pick, type Search, Tiles, tone, When } from "@/components/Section";
import { qs } from "@/lib/api";
import { apiGet } from "@/lib/server";
import type { Alerts } from "@/lib/types";
import { Reload } from "@/components/Reload";
import { buttonVariants } from "@/components/ui/button";
import { PlainCard } from "@/components/ui/card";

export const metadata: Metadata = { title: "Alerts" };

export default async function AlertsPage({ searchParams }: { searchParams: Search }) {
  const params = await pick(searchParams, ["q", "state", "sev", "dc", "sort", "page"]);
  const r = await apiGet<Alerts>("/api/v1/alerts" + qs(params));
  if (r.status !== 200) throw new Error(`/api/v1/alerts answered ${r.status}`);
  const d = r.data;
  return (
    <>
      <Reload r={d.loaded ? 0 : 60} />
      <header className="phead"><div><h1>Alerts</h1><HelpQ p="/alerts" />
        {d.loaded ? <p className="status-line"><b>{d.firing} firing</b><span className="sep">·</span>{d.pending} pending<span className="sep">·</span>updated <When ts={d.fetchedAt} /></p> : null}</div>
        <div className="phead-act"><a data-slot="button" className={buttonVariants({ variant: "ghost", size: "sm" })} href={d.list.path + (Object.keys(params).length ? qs(params) : "")}><Icon name="reset" />Refresh</a></div></header>
      {!d.loaded ? <Banner b={{ kind: "fail", text: `Alerts have not loaded: no snapshot exists yet. This page cannot say whether anything is firing. A snapshot is written every minute; if this stays, check that argus-alerts.timer runs on ${d.host}.` }} /> : null}
      {d.warn ? <Banner b={{ kind: "fail", text: d.warn }} /> : null}
      {d.loaded ? <Tiles tiles={d.tiles} /> : null}
      {d.loaded ? <Filters list={d.list} facets={d.facets} label="Search and filter alerts" placeholder="Alert name, instance, summary" /> : null}
      {d.loaded ? <p className="l-note">Live from the Prometheus of both datacenters ({d.sites}), refreshed every minute. Critical/page and error alerts are investigated automatically; warnings are listed here only.</p> : null}
      <PlainCard as="section" className="l-card" aria-label="Alerts">
        {d.rows.length ? (
          <div className="tablewrap" role="region" aria-label="Alerts table" tabIndex={0}><table className="l-table">
            <thead><tr><th scope="col">Alert</th><th scope="col">Severity</th><th scope="col">Since</th><th scope="col">DC</th><th scope="col">Instance</th><th scope="col" className="num">Value</th><th scope="col"><span className="sr">Incident</span></th></tr></thead>
            <tbody>{d.rows.map((a, i) => (
              <tr key={i}>
                <td className="t-main"><b>{a.name}</b>{a.pending ? <> <Pill>pending</Pill></> : null}{a.summary ? <span className="sub" dir="auto">{a.summary}</span> : null}</td>
                <td data-label="Severity"><Pill tone={tone(a.sevKind)}>{a.severity}</Pill></td>
                <td data-label="Since" className="nw muted" title={a.activeAt}>{a.since ? <When ts={a.since} /> : "-"}</td>
                <td data-label="DC">{a.dc}</td><td data-label="Instance" className="mono">{a.instance}</td><td data-label="Value" className="num">{a.value}</td>
                <td>{a.rca ? <a href={a.rca}>RCA</a> : null}</td>
              </tr>
            ))}</tbody>
          </table></div>
        ) : !d.loaded ? <p className="empty">No alert data yet, so nothing can be listed.</p>
          : d.list.filtered ? <p className="empty">No alert matches these filters. <a href={d.list.clearHref}>Clear the filters</a></p>
            : <p className="empty">No alert is firing in either datacenter.</p>}
      </PlainCard>
      <Pager list={d.list} />
    </>
  );
}

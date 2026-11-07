import { HelpQ } from "@/components/HelpQ";
import type { Metadata } from "next";
import { CostChart } from "@/components/Agent";
import { KindTag, Pill } from "@/components/Pills";
import { Filters, Pager, pick, type Search, tone, When } from "@/components/Section";
import { qs } from "@/lib/api";
import { apiGet } from "@/lib/server";
import type { Runs } from "@/lib/types";
import { PlainCard } from "@/components/ui/card";

export const metadata: Metadata = { title: "Agent runs" };

export default async function RunsPage({ searchParams }: { searchParams: Search }) {
  const r = await apiGet<Runs>("/api/v1/runs" + qs(await pick(searchParams, ["q", "task", "status", "sort", "page"])));
  if (r.status !== 200) throw new Error(`/api/v1/runs answered ${r.status}`);
  const d = r.data;
  return (
    <>
      <header className="phead"><div><h1>Agent runs</h1><HelpQ p="/runs" />
        <p className="meta-line"><span>{d.day} runs in 24 h</span><span>·</span>{d.dayBad ? <Pill tone="warn">{d.dayBad} not OK</Pill> : <span>all OK</span>}<span>·</span><span>cost, last 30 days: {d.cost}</span></p></div></header>
      <div className="stack">
        <PlainCard as="section" className="l-card" aria-labelledby="cost-h"><div className="card-h pad"><h2 id="cost-h">Cost per day (USD), last 30 days</h2></div>
          <CostChart days={d.days} top={d.dayTop} topLabel={d.dayTopLabel} /></PlainCard>
        {d.list.total > 0 ? <Filters list={d.list} facets={d.facets} label="Filter runs" placeholder="Task, status, summary or run number" /> : null}
        <PlainCard as="section" className="l-card" id="runs" aria-label="Runs">
          {d.rows.length ? (
            <div className="tablewrap" role="region" aria-label="Runs table" tabIndex={0}><table className="l-table">
              <thead><tr><th scope="col">Started</th><th scope="col">Task</th><th scope="col">Status</th><th scope="col" className="num">Duration</th><th scope="col" className="num">Cost</th><th scope="col" className="num">Turns</th><th scope="col" className="num" title="tool calls the permission rules refused">Denied</th><th scope="col">Summary</th></tr></thead>
              <tbody>{d.rows.map((x) => (
                <tr key={x.id}><td data-label="Started" className="nw"><When ts={x.at} /></td><td data-label="Task" className="nw"><KindTag>{x.task}</KindTag></td>
                  <td data-label="Status">{x.status ? <Pill tone={tone(x.status.kind)}>{x.status.text}</Pill> : null}</td>
                  <td data-label="Duration" className="num">{x.seconds} s</td><td data-label="Cost" className="num">{x.cost}</td><td data-label="Turns" className="num">{x.turns}</td>
                  <td data-label="Denied" className="num">{x.denied !== "0" ? <span className="r-denied">{x.denied}</span> : x.denied}</td>
                  <td className="t-main"><a href={`/runs/view?id=${x.id}`} dir="auto">{x.summary || `Run ${x.id}`}</a></td></tr>
              ))}</tbody></table></div>
          ) : d.list.total ? <p className="empty pad">No run matches these filters. <a href={d.list.clearHref}>Clear the filters</a></p> : <p className="empty pad">No runs yet. Each Argus task run is recorded here.</p>}
          <Pager list={d.list} />
        </PlainCard>
      </div>
    </>
  );
}

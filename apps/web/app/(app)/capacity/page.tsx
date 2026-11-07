import { HelpQ } from "@/components/HelpQ";
import type { Metadata } from "next";
import { capacityAnalyse } from "../inventory/actions";
import { Meter, P, Reload, Scan } from "@/components/Infra";
import { EnvPill, Pill } from "@/components/Pills";
import { Banners, Filters, Hidden, Pager, pick, type Search, Tiles, When } from "@/components/Section";
import { qs } from "@/lib/api";
import { apiGet, csrfToken, readFlash } from "@/lib/server";
import type { Capacity, CapacityFs, CapacityVm } from "@/lib/types";
import { Button } from "@/components/ui/button";
import { PlainCard } from "@/components/ui/card";

export const metadata: Metadata = { title: "Capacity" };

const SHOW = 15;

function FsRow({ f, csrf }: { f: CapacityFs; csrf: string }) {
  const a = f.analysis;
  return (
    <tr>
      <td><P p={f.level} />{f.new ? <> <Pill>new</Pill></> : null}</td>
      <td className="t-main nw">{f.href ? <a href={f.href} title={f.host}>{f.name}</a> : <b>{f.name}</b>}<span className="sub">{f.env ? <EnvPill env={f.env} /> : null} {f.dc}</span></td>
      <td data-label="Mount" className="mono nw">{f.mount}</td><td data-label="Used"><Meter m={f.used} /></td><td data-label="Free" className="num nw">{f.free}</td>
      <td data-label="Growth / day" className="num nw">{f.growth}</td><td data-label="Full in" className="num nw"><b>{f.days}</b></td>
      <td data-label="Analysis">
        {a.note ? <span className="muted small">{a.note}</span> : null}
        {a.report ? <span className="sub"><a href={a.report.href} title={a.report.title}><When ts={a.report.at} /></a> <span className="muted">{a.report.who}</span>{a.report.board?.href ? <> · <a href={a.report.board.href}>{a.report.board.text}</a></> : null}</span> : null}
        {a.show ? (a.can ? <form action={capacityAnalyse}><Hidden csrf={csrf} fs={a.fs} /><Button variant="outline" size="sm" title="What is filling it and the options (read-only, about 10 min, up to $1)">Analyse</Button></form> : <Button variant="outline" size="sm" disabled>Analyse</Button>) : null}
        {a.msg ? <span className="sub">{a.msg}</span> : null}
        {!a.note && !a.report && !a.show ? <span className="muted">-</span> : null}
      </td>
    </tr>
  );
}

const FsHead = () => <thead><tr><th scope="col">Level</th><th scope="col">Host · env · DC</th><th scope="col">Mount</th><th scope="col">Used</th><th scope="col" className="num">Free</th><th scope="col" className="num" title="Average daily growth of used space over the last 7 days">Growth / day</th><th scope="col" className="num" title="Days until the filesystem is full at this growth: free ÷ growth per day">Full in</th><th scope="col" title="What is filling it and the options (read-only)">Analysis</th></tr></thead>;

function VmTable({ rows, label }: { rows: CapacityVm[]; label: string }) {
  return (
    <div className="tablewrap" role="region" aria-label={label} tabIndex={0}><table className="l-table">
      <thead><tr><th scope="col">VM · env · DC</th><th scope="col">Level</th><th scope="col">Worst disk</th><th scope="col">Used</th><th scope="col" className="num">CPU busy, p95 7 d</th><th scope="col" className="num">Memory used, p95 7 d</th></tr></thead>
      <tbody>{rows.map((v, i) => (
        <tr key={i}>
          <td className="t-main nw">{v.href ? <a href={v.href}>{v.name}</a> : <b>{v.name}</b>}<span className="sub">{v.env ? <><EnvPill env={v.env} /> </> : null}{v.bits}</span>{v.hit ? <span className="sub">{v.hit}</span> : null}</td>
          <td data-label="Level"><P p={v.level} /></td>
          {!v.monitored ? <td colSpan={4} className="muted small">{v.why}</td> : <>
            <td data-label="Worst disk">{v.disk ? <><span className="mono">{v.disk.mount}</span><span className="sub">{v.disk.sub}</span>
              {v.disk.filesystems.length ? <details className="fold-t"><summary>All {v.disk.filesystems.length} filesystems</summary><ul className="list-plain small">{v.disk.filesystems.map((f) => <li key={f.mount}><span className="mono">{f.mount}</span> <span className="muted">{f.used} · {f.growth} · {f.days}</span></li>)}</ul></details> : null}</> : <span className="muted">-</span>}</td>
            <td data-label="Used"><Meter m={v.disk?.used ?? null} /></td>
            <td data-label="CPU busy" className="num nw"><span className={v.cpu?.hot ? `hot-${v.cpu.hot}` : undefined}>{v.cpu?.text}</span><span className="sub">{v.cpu?.sub}</span></td>
            <td data-label="Memory used" className="num nw"><span className={v.memory?.hot ? `hot-${v.memory.hot}` : undefined}>{v.memory?.text}</span><span className="sub">{v.memory?.sub}</span></td></>}
        </tr>
      ))}</tbody>
    </table></div>
  );
}

export default async function CapacityPage({ searchParams }: { searchParams: Search }) {
  const params = await pick(searchParams, ["q", "env", "dc", "vlevel", "vsort", "level", "page", "sent"]);
  const [r, csrf, flash] = await Promise.all([apiGet<Capacity>("/api/v1/capacity" + qs(params)), csrfToken(), readFlash()]);
  if (r.status !== 200) throw new Error(`/api/v1/capacity answered ${r.status}`);
  const d = r.data;
  const first = d.filesystems.slice(0, SHOW);
  const rest = d.filesystems.slice(SHOW);
  return (
    <>
      <Reload r={d.refresh} />
      <header className="phead"><div><h1>Capacity</h1><HelpQ p="/capacity" />
        {d.sub.length ? <p className="status-line">{d.sub.map((s, i) => <span key={s}>{i ? <span className="sep">·</span> : null}{s}</span>)}</p> : null}</div>
        <div className="phead-act"><Scan s={d.scan} csrf={csrf} /></div></header>
      <Banners list={d.banners} flash={flash} />
      {d.hasVMs ? <Tiles tiles={d.tiles} /> : null}
      <PlainCard as="section" className="l-card" id="fs" aria-labelledby="cf-h">
        <div className="card-h rows-h"><h2 id="cf-h">Filesystems that need attention <span className="ncount">{d.filesystems.length}</span></h2>
          <nav className="legend" aria-label="Show"><a href={d.show.riskHref} aria-current={d.show.level === "soon" ? "true" : undefined}>At risk ({d.show.risk})</a><a href={d.show.allHref} aria-current={d.show.level === "all" ? "true" : undefined}>All listed, 60 days ({d.show.all})</a></nav></div>
        {first.length ? <div className="tablewrap" role="region" aria-label="Filesystems" tabIndex={0}><table className="l-table"><FsHead /><tbody>{first.map((f, i) => <FsRow key={i} f={f} csrf={csrf} />)}</tbody></table></div>
          : <p className="empty">No filesystem is forecast to fill up within 30 days.</p>}
        {rest.length ? <details className="fold-t fold-c"><summary>{rest.length} more filesystems</summary><div className="tablewrap" role="region" aria-label="More filesystems" tabIndex={0}><table className="l-table"><FsHead /><tbody>{rest.map((f, i) => <FsRow key={i} f={f} csrf={csrf} />)}</tbody></table></div></details> : null}
      </PlainCard>
      <PlainCard as="section" className="l-card" aria-labelledby="ch-h"><div className="card-h rows-h"><h2 id="ch-h">Hot VMs <span className="ncount">{d.hot.length}</span></h2></div>
        {d.hot.length ? <VmTable rows={d.hot} label="Hot VMs" /> : <p className="empty">{d.hotNote}</p>}</PlainCard>
      {d.hasVMs ? (
        <div className="sec" id="vms">
          <h2>All VMs <span className="ncount">{d.vmTotal}</span></h2>
          <Filters list={d.list} facets={d.facets} label="Find a VM" placeholder="Name, IP, env, DC, role, service, domain or mount" />
          <PlainCard as="section" className="l-card" aria-label="All VMs">
            {d.rows.length ? <VmTable rows={d.rows} label="All VMs table" /> : <p className="empty">No VM matches these filters. <a href={d.list.clearHref}>Clear the filters</a></p>}
          </PlainCard>
          <Pager list={d.list} />
        </div>
      ) : null}
      <p className="l-note">Full in = free space ÷ growth per day. Growth per day is the average daily growth of used space over the last 7 days (a least-squares trend), for every filesystem on both datacenters; “shrinking” means used space went down. A filesystem whose used space is not growing is never “full in”. CPU busy, memory used, load per vCPU and the busiest disk&apos;s IO time are the average and p95 of 5-minute rates sampled hourly over 7 days. A VM&apos;s level is the worst of its filesystems, CPU and memory; “not monitored” is an Inventory VM with no node_exporter data. Updated daily at 08:30; no model involved. Analysis: what uses the space, what grows, and the options (cleanup, config, move, grow), read-only; it also runs by itself for a filesystem full within 14 days or 85 % used, at most once a day.</p>
    </>
  );
}

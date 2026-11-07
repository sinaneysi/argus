import { HelpQ } from "@/components/HelpQ";
import type { Metadata } from "next";
import { redirect } from "next/navigation";
import { At, Reload, Scan } from "@/components/Infra";
import { EnvPill, Pill } from "@/components/Pills";
import { Banners, Filters, Pager, pick, type Search, Tiles, tone, When } from "@/components/Section";
import { qs } from "@/lib/api";
import { safeNext } from "@/lib/format";
import { apiGet, csrfToken, readFlash } from "@/lib/server";
import type { Inventory } from "@/lib/types";
import { PlainCard } from "@/components/ui/card";

export const metadata: Metadata = { title: "Inventory" };

export default async function InventoryPage({ searchParams }: { searchParams: Search }) {
  const params = await pick(searchParams, ["q", "view", "env", "dc", "flag", "sort", "page", "go"]);
  const [r, csrf, flash] = await Promise.all([apiGet<Inventory>("/api/v1/inventory" + qs(params)), csrfToken(), readFlash()]);
  if (r.status !== 200) throw new Error(`/api/v1/inventory answered ${r.status}`);
  const d = r.data;
  if (d.goto) redirect(safeNext(d.goto, "/inventory"));
  return (
    <>
      <Reload r={d.refresh} />
      <header className="phead"><div><h1>Inventory</h1><HelpQ p="/inventory" />
        {d.total ? <p className="status-line"><b>{d.needs} of {d.total} VMs need a look</b><span className="sep">·</span>services checked <At ts={d.servicesAt} /><span className="sep">·</span>{d.pressure}</p> : null}</div>
        <div className="phead-act"><Scan s={d.scan} csrf={csrf} /></div></header>
      <Banners flash={flash} />
      <Tiles tiles={d.tiles} />
      <Filters list={d.list} facets={d.facets} label="Search and filter VMs" placeholder="Name, IP, env, DC, role, service, domain or mount" />
      <p className="l-note"><a href={d.csv}>Download as CSV</a></p>
      <PlainCard as="section" className="l-card" aria-label="VMs">
        {d.rows.length ? (
          <div className="tablewrap" role="region" aria-label="VMs table" tabIndex={0}><table className="l-table">
            <thead><tr><th scope="col">VM · IP</th><th scope="col">Env · DC</th><th scope="col" title="CPU, memory, IO, disk: the worst of them now (15-minute average)">Pressure</th><th scope="col">Services</th>
              <th scope="col" className="num" title="Ports nginx routes to on this VM where nothing listens">Dead upstreams</th><th scope="col" title="Open vulnerabilities: P1 and P2 first">CVE</th><th scope="col">Last analysis</th>
              <th scope="col" className="num lo">vCPU</th><th scope="col" className="num lo">RAM</th><th scope="col" className="num">Disk used</th><th scope="col" className="num">Uptime</th></tr></thead>
            <tbody>{d.rows.map((v) => (
              <tr key={v.id}>
                <td className="t-main nw"><a href={`/inventory/view?id=${v.id}`}>{v.name}</a>
                  <span className="sub mono">{v.ip}{v.ipMore ? ` +${v.ipMore}` : ""}</span>
                  {v.hit ? <span className="sub">{v.hit}</span> : null}
                  {v.flags.length ? <span className="sub">{v.flags.map((f) => <span key={f.label} title={f.value}><Pill>{f.label}</Pill> </span>)}</span> : null}</td>
                <td data-label="Env" className="nw"><EnvPill env={v.env} /><span className="sub">{v.dc}</span></td>
                <td data-label="Pressure" className="nw">{v.health.kind ? <Pill tone={tone(v.health.kind)} title={v.health.title}>{v.health.word}</Pill> : <span className="muted">{v.health.word}</span>}{v.health.detail ? <span className="sub">{v.health.detail}</span> : null}</td>
                <td data-label="Services" className={v.servicesFail ? "nw hot-fail" : "nw"}>{v.services}</td>
                <td data-label="Dead upstreams" className="num">{v.dead ? <b className="hot-fail">{v.dead}</b> : <span className="muted">0</span>}</td>
                <td data-label="CVE" className="nw">{v.cve}</td>
                <td data-label="Last analysis" className="nw">{v.analysis?.href ? <><a href={v.analysis.href} title={v.analysis.title}><When ts={v.analysis.at ?? 0} /></a><span className="sub">{v.analysis.who}</span></> : <span className="muted">{v.analysis?.note ?? "-"}</span>}</td>
                <td data-label="vCPU" className="num lo">{v.vcpu}</td><td data-label="RAM" className="num nw lo">{v.ram}</td>
                <td data-label="Disk used" className="num nw">{v.used}<span className="sub">{v.disk}</span></td><td data-label="Uptime" className="num nw muted">{v.uptime}</td>
              </tr>
            ))}</tbody>
          </table></div>
        ) : d.list.q ? <p className="empty"><b>No VM matches ‘{d.list.q}’.</b> Every word must match. <a href={d.list.clearHref}>Clear the search</a></p>
          : d.list.filtered ? <p className="empty">No VM matches these filters. <a href={d.list.clearHref}>Clear the filters</a></p>
            : <p className="empty">No VM seen yet. Argus lists VMs after the next scan.</p>}
      </PlainCard>
      <Pager list={d.list} />
      <p className="l-note">Every VM Argus can see: node_exporter targets of both Prometheus servers and the hosts behind the read-only ssh aliases (DB hosts are never logged in to). Pressure is the worst of CPU, memory, IO and disk now (15-minute average), re-checked every 5 minutes; services, ports and upstreams every 6 hours and on Refresh. A dead upstream is a port nginx routes to on this VM where nothing listens. Worst first: critical resources, services down or restarting, dead upstreams. Inventory facts <At ts={d.factsAt} />.</p>
    </>
  );
}

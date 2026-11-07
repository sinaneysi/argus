import { HelpQ } from "@/components/HelpQ";
import type { Metadata } from "next";
import { notFound } from "next/navigation";
import { vmAct } from "../actions";
import { At, Bar, Mini, Note, P, Reload } from "@/components/Infra";
import { EnvPill, Pill } from "@/components/Pills";
import { Banners, Crumb, FixCards, Hidden, Jump, one, ProblemBox, Rail, Report, type Search, Segs, Summary, tone, When } from "@/components/Section";
import { qs } from "@/lib/api";
import { apiGet, csrfToken, readFlash } from "@/lib/server";
import type { InventoryItem, Share, UpstreamRow } from "@/lib/types";
import { Button, buttonVariants } from "@/components/ui/button";
import { PlainCard } from "@/components/ui/card";
import { Connect } from "@/components/Connect";

export const metadata: Metadata = { title: "VM" };

function Upstreams({ rows }: { rows: UpstreamRow[] }) {
  return (
    <div className="tablewrap" role="region" aria-label="Upstreams" tabIndex={0}><table className="l-table">
      <thead><tr><th scope="col" className="num">Port</th><th scope="col">State</th><th scope="col">Listening unit</th><th scope="col">Routes (domain and location, and where the route comes from)</th></tr></thead>
      <tbody>{rows.map((u) => (
        <tr key={u.port}><td className="num t-main"><b>:{u.port}</b></td><td data-label="State"><P p={u.state} /></td><td data-label="Unit" className="mono">{u.unit}</td>
          <td data-label="Routes">{u.routes.map((x, i) => <span key={i} className="sub"><span className="mono">{x.value}</span> <span className="muted">{x.label}</span></span>)}{u.more ? <span className="sub">+{u.more} more routes</span> : null}</td></tr>
      ))}</tbody>
    </table></div>
  );
}

function Shares({ list }: { list: Share[] }) {
  if (!list.length) return <p className="muted small">Nothing busy.</p>;
  return (
    <ul className="share">{list.map((s, i) => (
      <li key={i}><span className="mono small unit">{s.unit}</span>
        <span className="meter"><Bar pct={s.pct} kind="ok" /><span className="small"><b>{s.figure}</b>{s.share ? ` · ${s.share}` : ""}</span></span>
        {s.note ? <span className="muted small">{s.note}</span> : null}</li>
    ))}</ul>
  );
}

export default async function VmPage({ searchParams }: { searchParams: Search }) {
  const sp = await searchParams;
  const params: Record<string, string> = { id: one(sp.id) };
  for (const k of ["sent", "pq"]) if (one(sp[k])) params[k] = one(sp[k]);
  const [r, csrf, flash] = await Promise.all([apiGet<InventoryItem>("/api/v1/inventory/item" + qs(params)), csrfToken(), readFlash()]);
  if (r.status === 404) notFound();
  if (r.status !== 200) throw new Error(`/api/v1/inventory/item answered ${r.status}`);
  const d = r.data;
  const a = d.actions;
  const l = d.analysis.latest;
  const v = d.vulns;
  return (
    <>
      <Reload r={d.refresh} />
      <header className="phead">
        <div><Crumb href="/inventory" label="Inventory" /><h1 dir="auto">{d.name}</h1><HelpQ p="/inventory" />
          <p className="meta-line"><EnvPill env={d.env} /><span>{d.dc}</span><b>{d.summary}</b><span>{d.state}</span></p></div>
        <div className="phead-act scanctl">
          <span className="muted small">collected <At ts={a.collected} />{a.refresh.note ? ` · ${a.refresh.note}` : ""}</span>
          {a.refresh.can ? <form action={vmAct}><Hidden csrf={csrf} id={d.id} what="refresh" /><Button variant="outline" title="Collect this VM again now: Prometheus and the read-only snapshot, no model (about a minute)">Refresh</Button></form>
            : <Button variant="outline" disabled>{a.refresh.busy ? "Refreshing…" : "Refresh"}</Button>}
          {a.analyse ? (a.analyse.can ? <form action={vmAct}><Hidden csrf={csrf} id={d.id} what="analyse" /><Button  title={a.analyse.title}>Analyse</Button></form>
            : <Button variant="outline" disabled>{a.analyse.busy ? "Analysing…" : "Analyse"}</Button>) : null}
          {a.analyse?.note ? <span className="muted small">{a.analyse.note}</span> : null}
        </div>
      </header>
      <Banners list={d.banners} flash={flash} />
      <div className="tiles res-tiles">
        {d.resources.map((x) => (
          <div key={x.label} className="tile"><span className="t-k">{x.label}{x.state ? <Pill tone={tone(x.kind)}>{x.state}</Pill> : null}</span>
            <b className="t-f num">{x.figure} <small>{x.unit}</small></b><span className="t-s" title={x.title}>{x.note}</span></div>
        ))}
        <a className="tile" href="#services"><span className="t-k">Services ready</span><b className={d.ready.hot ? "t-f num hot" : "t-f num"}>{d.ready.figure}{d.ready.of ? <small> of {d.ready.of}</small> : null}</b><span className="t-s">{d.ready.note}</span></a>
      </div>
      <Jump items={d.jump} />
      <div className="detail">
        <div className="stack">
          <Connect id="connect" steps={d.connect} via={d.connectVia} />
          {d.incidents.length ? (
            <PlainCard as="section" className="l-card" id="incidents" aria-labelledby="vi-h"><div className="card-h rows-h"><h2 id="vi-h">Open incidents <span className="ncount">{d.incidents.length}</span></h2><a className="small" href="/incidents">Incidents</a></div>
              <ul className="rows">{d.incidents.map((i) => <li key={i.id}><p className="th"><Pill tone={tone(i.kind)}>{i.status}</Pill><a href={`/incidents/view?id=${i.id}`} dir="auto">{i.title}</a><span className="muted small">#{i.id}</span></p></li>)}</ul></PlainCard>
          ) : null}
          <PlainCard as="section" className="l-card" id="services" aria-labelledby="vs-h"><div className="card-h rows-h"><h2 id="vs-h">Services <span className="ncount">{d.serviceCount}</span></h2></div>
            {d.services.rows.length ? (
              <div className="tablewrap" role="region" aria-label="Services table" tabIndex={0}><table className="l-table">
                <thead><tr><th scope="col">Service</th><th scope="col">Ready</th><th scope="col" className="num">Memory</th><th scope="col" className="num">CPU</th><th scope="col" className="num" title="Automatic restarts since it was last started by hand">Restarts</th><th scope="col" className="num">Up for</th><th scope="col">Ports</th><th scope="col" className="num">Errors 1 h</th><th scope="col">Routes from nginx</th></tr></thead>
                <tbody>{d.services.rows.map((s) => (
                  <tr key={s.unit}>
                    <td className="t-main"><span className="mono unit">{s.unit}</span>{s.kind ? <> <Pill>{s.kind}</Pill></> : null}{s.why ? <span className="sub">{s.why}</span> : null}</td>
                    <td data-label="Ready"><P p={s.verdict} /></td>
                    <td data-label="Memory" className="num nw">{s.memory}{s.memoryNote ? <span className="sub">{s.memoryNote}</span> : null}</td>
                    <td data-label="CPU" className="num nw">{s.cpu}</td><td data-label="Restarts" className="num">{s.restarts}</td><td data-label="Up for" className="num nw">{s.up}</td>
                    <td data-label="Ports" className="nw">{s.ports.join(" ")} {s.deadPorts.map((p) => <b key={p} className="hot-fail" title="nginx routes here, nothing listens">{p} </b>)}{!s.ports.length && !s.deadPorts.length ? <span className="muted">-</span> : null}</td>
                    <td data-label="Errors 1 h" className="num">{s.errors}</td>
                    <td data-label="Routes" title={s.routes.join(", ")}>{s.routes[0] ?? <span className="muted">-</span>}{s.routes.length > 1 ? <span className="sub">+{s.routes.length - 1} more</span> : null}</td>
                  </tr>
                ))}</tbody>
              </table></div>
            ) : <p className="empty">No service data yet. Refresh collects it when the VM has an ssh-ro alias.</p>}
            {d.services.idle.length ? <details className="fold-t fold-c"><summary>{d.services.idle.length} idle units (one-shot or disabled, not counted)</summary><p className="pad muted small">{d.services.idle.join(", ")}</p></details> : null}
            <p className="pad muted small card-gap">{d.services.source}</p>
          </PlainCard>
          <PlainCard as="section" className="l-card" id="upstreams" aria-labelledby="vu-h"><div className="card-h rows-h"><h2 id="vu-h">Upstreams <span className="ncount">{d.upstreams.count}</span>{d.upstreams.deadCount ? <Pill tone="fail">{d.upstreams.deadCount} dead</Pill> : null}</h2></div>
            <p className="pad muted small">Ports on this VM that nginx sends traffic to: the nginx repo&apos;s service map and the live config of every nginx host Argus can read. Dead means nothing listens on that port now, so those routes answer 502.</p>
            {!d.upstreams.count ? <p className="empty">No nginx route (the repo service map or a live nginx config) points at this VM.</p>
              : d.upstreams.dead.length ? <Upstreams rows={d.upstreams.dead} /> : <p className="empty">No dead upstream: every port the live nginx config routes to on this VM is listening.</p>}
            {d.upstreams.ok.length ? <details className="fold-t fold-c"><summary>{d.upstreams.ok.length} listening upstreams</summary><Upstreams rows={d.upstreams.ok} /></details> : null}
            {d.upstreams.repo.length ? <details className="fold-t fold-c"><summary>{d.upstreams.repo.length} routes only in the nginx repo (not counted)</summary><Upstreams rows={d.upstreams.repo} /></details> : null}
            {d.serves ? <p className="pad muted small card-gap">{d.serves}</p> : null}{d.dataRoles ? <p className="pad muted small card-gap">{d.dataRoles}</p> : null}
          </PlainCard>
          {l ? <Summary l={l.lazy} /> : null}
          <PlainCard as="section" id="analysis" aria-labelledby="va-h"><div className="card-h"><h2 id="va-h">Analysis</h2></div>
            {l ? (
              <div className="stack">
                <p className="meta-line"><Pill tone={tone(l.severityKind)}>{l.severity}</Pill><b dir="auto">{l.title}</b></p>
                <p className="muted small"><When ts={l.at} /> · {l.who} · {l.mode} · {l.cost} · <a href={l.href}>Open as a problem page</a>{!l.prod ? <> · <a href={l.href}>Review the fix prompt</a></> : null}</p>
                {l.board.href ? <p><a data-slot="button" className={buttonVariants({ variant: "outline", size: "sm" })} href={l.board.href}>{l.board.text}</a></p> : null}
                {l.prod ? <Banners list={[{ text: "Production: every option in this report is a proposal; each change needs the operator's explicit approval.", kind: "info" }]} /> : null}
                {l.summary ? <p dir="auto">{l.summary}</p> : null}
                <Report doc={l.report} folded={l.folded} />
                {l.more.length ? <><h3>Separate fixes</h3><ul className="list-plain">{l.more.map((m) => <li key={m.href}><a href={m.href}>{m.title}</a> <span className="muted small">{m.severity} · its own fix prompt</span></li>)}</ul></> : null}
              </div>
            ) : !d.analysis.history.length ? <p className="empty"><b>No analysis yet.</b> Analyse (admins) investigates read-only: the resource under pressure, who uses it, whether that is needed or a leak, and why services are not ready. It also runs by itself when memory, CPU or IO stays critical for 15 minutes, a service goes down or a dead upstream appears.</p> : null}
            {d.analysis.history.length ? (
              <details className="fold-t"><summary>History <span className="ncount">{d.analysis.history.length}</span></summary>
                <div className="tablewrap" role="region" aria-label="Analysis history" tabIndex={0}><table className="l-table">
                  <thead><tr><th scope="col">When</th><th scope="col">Result</th><th scope="col">Report</th><th scope="col">By</th><th scope="col" className="num">Cost</th></tr></thead>
                  <tbody>{d.analysis.history.map((h, i) => <tr key={i}><td className="nw t-main"><When ts={h.at} /></td><td data-label="Result"><P p={h.status} /></td><td data-label="Report">{h.href ? <a href={h.href}>{h.report}</a> : h.report}</td><td data-label="By" className="nw">{h.who}{h.trigger ? <span className="sub">{h.trigger}</span> : null}</td><td data-label="Cost" className="num">{h.cost}</td></tr>)}</tbody>
                </table></div></details>
            ) : null}
          </PlainCard>
          {l?.fix ? <FixCards f={l.fix} quiet /> : null}
          <ProblemBox p={d.problem} csrf={csrf} />
          <PlainCard as="section" className="l-card" id="vulns" aria-labelledby="vv-h"><div className="card-h rows-h"><h2 id="vv-h">Vulnerabilities <span className="ncount">{v.components.length}</span></h2></div>
            {v.note ? <p className="pad muted small">{v.note}</p> : null}
            {v.services.length ? <><h3 className="grp">By service on this VM</h3>
              <div className="tablewrap" role="region" aria-label="Vulnerabilities by service" tabIndex={0}><table className="l-table">
                <thead><tr><th scope="col">Priority</th><th scope="col">Service</th><th scope="col" className="num">Vulns</th><th scope="col">Exposure</th><th scope="col">Components</th></tr></thead>
                <tbody>{v.services.map((s) => <tr key={s.service}><td><P p={s.priority} /></td><td className="t-main">{s.href ? <a href={s.href}>{s.service}</a> : s.service}<span className="sub mono">{s.units}</span></td><td data-label="Vulns" className="num">{s.vulns}{s.kev ? <> <Pill tone="fail">KEV {s.kev}</Pill></> : null}</td><td data-label="Exposure">{s.exposure}</td><td data-label="Components"><Segs s={s.components.flatMap((c, i) => (i ? [{ text: ", " }, c] : [c]))} /></td></tr>)}</tbody>
              </table></div></> : null}
            {v.containers.length ? <><h3 className="grp">Containers on this VM</h3>
              <div className="tablewrap" role="region" aria-label="Containers" tabIndex={0}><table className="l-table">
                <thead><tr><th scope="col">Container</th><th scope="col">Image</th><th scope="col">Seen by</th><th scope="col">Inside</th><th scope="col" className="num">Open vulns</th></tr></thead>
                <tbody>{v.containers.map((c) => <tr key={c.name}><td className="t-main">{c.name}</td><td data-label="Image" className="mono">{c.image}{c.declared ? <span className="sub">declared in config, not observed</span> : null}</td><td data-label="Seen by">{c.source}</td><td data-label="Inside">{c.inside}</td><td data-label="Open vulns" className="num">{c.vulns}</td></tr>)}</tbody>
              </table></div></> : null}
            {v.components.length ? <>{v.services.length || v.containers.length ? <h3 className="grp">By component</h3> : null}
              <div className="tablewrap" role="region" aria-label="Vulnerabilities by component" tabIndex={0}><table className="l-table">
                <thead><tr><th scope="col">Priority</th><th scope="col">Component</th><th scope="col" className="num">Vulns</th><th scope="col">Fixed in</th></tr></thead>
                <tbody>{v.components.map((c) => <tr key={c.href}><td><P p={c.priority} /></td><td className="t-main"><a href={c.href}>{c.name}</a> <span className="muted mono">{c.version}</span>{c.kev ? <> <Pill tone="fail">KEV</Pill></> : null}<span className="sub">{c.kind}</span></td><td data-label="Vulns" className="num">{c.vulns}</td><td data-label="Fixed in" className="mono">{c.fixed}</td></tr>)}</tbody>
              </table></div></> : <p className="empty">No open vulnerability on this VM.</p>}
          </PlainCard>
          <PlainCard as="section" id="resources" aria-labelledby="vr-h"><div className="card-h"><h2 id="vr-h">Who uses the resources</h2></div>
            {d.consumers ? <div className="halves"><div><h3>Memory</h3><Shares list={d.consumers.memory} /></div><div><h3>CPU, over 3 seconds</h3><Shares list={d.consumers.cpu} /></div></div>
              : <p className="empty">No per-unit numbers without an ssh-ro snapshot.</p>}
          </PlainCard>
          <PlainCard as="section" className="l-card" aria-labelledby="vp-h"><div className="card-h rows-h"><h2 id="vp-h">Pressure</h2></div>
            {d.pressure.length ? (
              <div className="tablewrap" role="region" aria-label="Pressure table" tabIndex={0}><table className="l-table">
                <thead><tr><th scope="col">Measure</th><th scope="col" className="num">Now</th><th scope="col" className="num">24 h max</th><th scope="col">Last 24 h</th><th scope="col">Last 7 days</th></tr></thead>
                {d.pressure.map((g) => (
                  <tbody key={g.name}>
                    <tr className="grp-row"><th colSpan={5} scope="rowgroup">{g.name} {g.state ? <Pill tone={tone(g.kind)}>{g.state}</Pill> : null} {g.why ? <span className="muted small">{g.why}</span> : null}</th></tr>
                    {g.rows.map((x) => <tr key={x.label}><td className="t-main">{x.label}{x.limits ? <span className="sub">{x.limits}</span> : null}</td><td data-label="Now" className="num nw"><b className={x.hot ? `hot-${x.hot}` : undefined}>{x.now}</b></td><td data-label="24 h max" className="num nw">{x.max}</td><td data-label="Last 24 h"><Mini m={x.day} /></td><td data-label="Last 7 days"><Mini m={x.week} /></td></tr>)}
                  </tbody>
                ))}
              </table></div>
            ) : <p className="empty"><b>No Prometheus series for this VM.</b> It is not a node_exporter target of either DC.</p>}
            {d.pressure.length ? <p className="pad muted small card-gap">Now is the last 15-minute average; the dashed line is the pressure threshold. The 7-day line is a 15-minute average every 2 hours. PSI is the share of time tasks waited for the resource (Linux pressure stall information).</p> : null}
          </PlainCard>
          <PlainCard as="section" className="l-card" id="filesystems" aria-labelledby="vf-h"><div className="card-h rows-h"><h2 id="vf-h">Filesystems</h2></div>
            {d.mounts.length ? (
              <div className="tablewrap" role="region" aria-label="Filesystems table" tabIndex={0}><table className="l-table">
                <thead><tr><th scope="col">Mount</th><th scope="col" className="num">Size</th><th scope="col">Used</th><th scope="col" className="num">Inodes</th><th scope="col" className="num" title="Average daily growth of used space over the last 7 days">Growth / day</th><th scope="col" className="num" title="Days until the filesystem is full at this growth: free ÷ growth per day">Days to full</th></tr></thead>
                <tbody>{d.mounts.map((m) => <tr key={m.mount}><td className="t-main mono">{m.mount}</td><td data-label="Size" className="num nw">{m.size}</td><td data-label="Used"><span className="meter"><Bar pct={m.pct} kind={m.kind} /><span className="num">{m.used}</span></span></td><td data-label="Inodes" className="num">{m.inodes}</td><td data-label="Growth / day" className="num">{m.growth}</td><td data-label="Days to full" className="num">{m.days}</td></tr>)}</tbody>
              </table></div>
            ) : <p className="empty">No mount data.</p>}
          </PlainCard>
          <PlainCard as="section" id="history" aria-labelledby="vh-h"><div className="card-h"><h2 id="vh-h">History and raw facts</h2></div>
            <details className="fold-t"><summary>Change history <span className="ncount">{d.changes.length}</span></summary>
              {d.changes.length ? <div className="tablewrap" role="region" aria-label="Change history" tabIndex={0}><table className="l-table"><thead><tr><th scope="col">When</th><th scope="col">What</th><th scope="col">Old</th><th scope="col">New</th></tr></thead>
                <tbody>{d.changes.map((c, i) => <tr key={i}><td className="nw t-main"><When ts={c.at} /></td><td data-label="What">{c.field}</td><td data-label="Old" className="mono">{c.old}</td><td data-label="New" className="mono">{c.new}</td></tr>)}</tbody></table></div> : <p className="empty">No change recorded.</p>}</details>
            <details className="fold-t"><summary>Raw facts (ssh-ro)</summary>
              {d.raw.length ? <dl className="rec wide">{d.raw.map((x) => <div key={x.label}><dt>{x.label}</dt><dd className="mono">{x.value}</dd></div>)}</dl> : <p className="empty">No ssh facts.</p>}</details>
          </PlainCard>
        </div>
        <aside id="record">
          <Rail facts={d.identity} title="Identity" />
          <Rail facts={d.access} title="Access" />
          {d.notes.map((n, i) => <Note key={i}>{n}</Note>)}
        </aside>
      </div>
    </>
  );
}

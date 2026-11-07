import { HelpQ } from "@/components/HelpQ";
import type { Metadata } from "next";
import { notFound } from "next/navigation";
import { AffectedTable, Exposure } from "@/components/Delivery";
import { Icon } from "@/components/Icon";
import { EnvPill, Pill } from "@/components/Pills";
import { Banners, FixCards, Hidden, one, pick, ProblemBox, type Search, Crumb, Summary, Tiles, tone } from "@/components/Section";
import { qs } from "@/lib/api";
import { apiGet, csrfToken, readFlash } from "@/lib/server";
import type { Banner, VulnGroup, VulnGroupRow, Vulns } from "@/lib/types";
import { vulnSuppress } from "../cicd/actions";
import { Button, buttonVariants } from "@/components/ui/button";
import { PlainCard } from "@/components/ui/card";

export const metadata: Metadata = { title: "Vulnerabilities" };

function GroupTable({ rows, label, empty }: { rows: VulnGroupRow[]; label: string; empty: React.ReactNode }) {
  if (!rows.length) return <p className="empty">{empty}</p>;
  return (
    <div className="tablewrap" role="region" aria-label={label} tabIndex={0}><table className="l-table">
      <thead><tr><th scope="col">Priority</th><th scope="col">Component</th><th scope="col">Env class</th><th scope="col" className="num">Vulns</th><th scope="col" className="num">Targets</th><th scope="col">Fixed in</th><th scope="col" className="num">SLA due</th></tr></thead>
      <tbody>{rows.map((g) => (
        <tr key={g.href}>
          <td><Pill tone={tone(g.priKind)} title="priority">{g.pri || "?"}</Pill></td>
          <td className="t-main"><a href={g.href}>{g.name}</a> <span className="muted small">{g.versions}</span>
            {g.kev ? <> <Pill tone="fail" title="in CISA KEV: exploited in the wild">KEV {g.kev}</Pill></> : null}{g.internet ? <> <Pill title="a target is internet-facing">internet</Pill></> : null}
            <span className="sub">{g.kind}{g.ecosystem ? ` · ${g.ecosystem}` : ""}</span>
            {g.affected.length ? <details className="fold"><summary className="small">{g.affects}</summary><AffectedTable rows={g.affected} more={g.moreAffected} moreHref={g.moreHref} label={`Affected by ${g.name}`} /></details> : <span className="sub">{g.affects}</span>}</td>
          <td data-label="Env class" className="nw">{g.class}</td><td data-label="Vulns" className="num">{g.vulns}</td><td data-label="Targets" className="num">{g.targets}</td>
          <td data-label="Fixed in" className="nw mono">{g.fixed || <span className="muted">no fix yet</span>}</td>
          <td data-label="SLA due" className={g.late ? "num nw hot-fail" : "num nw"}>{g.due ? (g.late ? `${g.due} · overdue` : g.due) : "-"}</td>
        </tr>
      ))}</tbody>
    </table></div>
  );
}

function Pick({ name, label, value, options }: { name: string; label: string; value: string; options: string[] }) {
  return <label className={value ? "f-pill on" : "f-pill"}><span className="f-l">{label}</span><select name={name} defaultValue={value}><option value="">All</option>{options.map((o) => <option key={o} value={o}>{o}</option>)}</select></label>;
}

function Coverage({ c }: { c: Vulns["coverage"] }) {
  const k = c.containers;
  return (
    <PlainCard as="section" className="l-card" id="coverage" aria-labelledby="co-h">
      <div className="card-h rows-h"><h2 id="co-h">Coverage and feeds</h2></div>
      <p className="pad muted small">{c.scanned} of {c.hosts} hosts have a package list ({c.withAccess} have read-only access). Distinct versions: {c.kinds}. Feeds are fetched by root ({c.mode}); matching runs offline from that cache.</p>
      <details className="fold-t fold-c"><summary>Feed hosts <span className="ncount">{c.feedHosts.length}</span></summary>
        {c.feedHosts.length ? <div className="tablewrap" role="region" aria-label="Feed hosts" tabIndex={0}><table className="l-table"><thead><tr><th scope="col">Host</th><th scope="col">State</th><th scope="col" className="num">Requests</th><th scope="col">Error</th></tr></thead>
          <tbody>{c.feedHosts.map((h) => <tr key={h.host}><td>{h.host}</td><td data-label="State">{h.state}</td><td data-label="Requests" className="num">{h.requests}</td><td data-label="Error">{h.error}</td></tr>)}</tbody></table></div> : <p className="empty">No fetch yet.</p>}
      </details>
      {c.queries ? <details className="fold-t fold-c" open={c.queries.failed.length > 0}><summary>Feed queries <span className="ncount">{c.queries.failed.length}</span></summary>
        <p className="pad muted small">{c.queries.line}. Requests this run: {c.queries.requests} (stale = the fetch failed and the previous good answer was kept).</p>
        {c.queries.failed.length ? <div className="tablewrap" role="region" aria-label="Failed feed queries" tabIndex={0}><table className="l-table"><thead><tr><th scope="col">Problem</th><th scope="col">Query</th><th scope="col">Why</th></tr></thead>
          <tbody>{c.queries.failed.map((f, i) => <tr key={i}><td className="nw">{f.kind}</td><td data-label="Query" className="mono">{f.subject}</td><td data-label="Why">{f.why}</td></tr>)}</tbody></table></div> : null}
        {c.queries.nvd.length ? <div className="tablewrap" role="region" aria-label="NVD products" tabIndex={0}><table className="l-table"><thead><tr><th scope="col">NVD product</th><th scope="col">State</th><th scope="col" className="num">CVEs</th><th scope="col" className="num">Pages</th><th scope="col">Why</th></tr></thead>
          <tbody>{c.queries.nvd.map((n) => <tr key={n.product}><td className="mono">{n.product}</td><td data-label="State" className={n.state === "ok" ? undefined : "hot-fail"}>{n.state}</td><td data-label="CVEs" className="num">{n.cves}</td><td data-label="Pages" className="num">{n.pages}</td><td data-label="Why">{n.why}</td></tr>)}</tbody></table></div> : null}
      </details> : null}
      <details className="fold-t fold-c"><summary>Hosts without a package list <span className="ncount">{c.missing.length}</span></summary>
        {c.missing.length ? <div className="tablewrap" role="region" aria-label="Hosts without a package list" tabIndex={0}><table className="l-table"><thead><tr><th scope="col">Host</th><th scope="col">Env</th><th scope="col">Why</th></tr></thead>
          <tbody>{c.missing.map((m) => <tr key={m.name}><td>{m.name}</td><td data-label="Env">{m.env ? <EnvPill env={m.env} /> : null}</td><td data-label="Why">{m.why}</td></tr>)}</tbody></table></div> : <p className="empty">None.</p>}
      </details>
      {k ? <>
        <h3 className="pad grp" id="containers">Containers and blind spots</h3>
        <p className="pad muted small">{k.note}</p>
        <details className="fold-t fold-c" open><summary>Blind spots <span className="ncount">{k.blind.length}</span></summary>
          {k.blind.length ? <><p className="pad muted small">What the scan cannot see, so the gap is explicit: {k.blindCounts}.</p>
            <div className="tablewrap" role="region" aria-label="Blind spots" tabIndex={0}><table className="l-table"><thead><tr><th scope="col">Blind spot</th><th scope="col">What</th><th scope="col">Host</th><th scope="col">Why</th></tr></thead>
              <tbody>{k.blind.map((b, i) => <tr key={i}><td className="nw">{b.label}</td><td data-label="What" className="mono">{b.subject}</td><td data-label="Host" className="nw">{b.hostHref ? <a href={b.hostHref}>{b.host}</a> : b.host}</td><td data-label="Why">{b.why}</td></tr>)}</tbody></table></div></> : <p className="empty">None.</p>}
        </details>
        <details className="fold-t fold-c"><summary>Running containers <span className="ncount">{k.rows.length}</span></summary>
          {k.rows.length ? <div className="tablewrap" role="region" aria-label="Running containers" tabIndex={0}><table className="l-table"><thead><tr><th scope="col">Host</th><th scope="col">Env</th><th scope="col">Container</th><th scope="col">Image</th><th scope="col">Seen by</th><th scope="col">Inside</th></tr></thead>
            <tbody>{k.rows.map((x, i) => <tr key={i}><td className="nw">{x.hostHref ? <a href={x.hostHref}>{x.host}</a> : x.host}</td><td data-label="Env">{x.env ? <EnvPill env={x.env} /> : null}</td><td data-label="Container">{x.name}</td><td data-label="Image" className="mono">{x.image}{x.digest ? <span className="sub">{x.digest}</span> : null}</td><td data-label="Seen by">{x.source}</td><td data-label="Inside">{x.inside}</td></tr>)}</tbody></table></div> : <p className="empty">No container known yet.</p>}
        </details>
      </> : null}
    </PlainCard>
  );
}

function List({ d, flash }: { d: Vulns; flash: Banner | null }) {
  const s = d.sel;
  return (
    <>
      <header className="phead"><div><h1>Vulnerabilities</h1><HelpQ p="/vulns" />
        {d.scanned ? <p className="meta-line"><b>{d.p1} P1</b><span>{d.p2} P2</span><span>{d.kev} known exploited</span><span>{d.components} vulnerable components</span><span>feed data from {d.feedAt}</span></p>
          : <p className="meta-line"><span>No scan yet: is argus-cve.timer installed on {d.host}?</span></p>}</div></header>
      <Banners list={d.banners} flash={flash} />
      <Tiles tiles={d.tiles} />
      <PlainCard as="section" className="l-card" aria-labelledby="ff-h">
        <div className="card-h rows-h"><h2 id="ff-h">Fix first</h2></div>
        <p className="pad muted small">The ten component groups with the highest priority and score that have a fix today (vulnerabilities with no released fix are listed under By component, marked no fix yet). Each opens its vulnerabilities, who is affected, how to fix it and a prompt for an agent with write access.</p>
        <GroupTable rows={d.fixFirst} label="Fix first" empty={d.scanned ? "Nothing open." : "No scan yet, so there is nothing to show."} />
      </PlainCard>
      {d.risk.length ? (
        <PlainCard as="section" className="l-card" id="services" aria-labelledby="sr-h">
          <div className="card-h rows-h"><h2 id="sr-h">Services most at risk</h2></div>
          <p className="pad muted small">Per service, the open P1 and P2 vulnerabilities that reach it on its hosts and the known exploited ones. Host-wide items (kernel, end-of-life OS) are listed per component, not here.</p>
          <div className="tablewrap" role="region" aria-label="Services most at risk table" tabIndex={0}><table className="l-table">
            <thead><tr><th scope="col">Service</th><th scope="col" className="num">Hosts</th><th scope="col" className="num">P1</th><th scope="col" className="num">P2</th><th scope="col" className="num">KEV</th><th scope="col">Exposure</th><th scope="col">Fix first</th></tr></thead>
            <tbody>{d.risk.map((r) => <tr key={r.service} aria-current={r.on ? "true" : undefined}><td className="t-main"><a href={r.href}>{r.service}</a></td><td data-label="Hosts" className="num">{r.hosts}<span className="sub">{r.hostList}</span></td><td data-label="P1" className={r.p1 ? "num hot-fail" : "num"}>{r.p1}</td><td data-label="P2" className="num">{r.p2}</td><td data-label="KEV" className="num">{r.kev ? <Pill tone="fail">{r.kev}</Pill> : 0}</td><td data-label="Exposure"><Exposure e={r.exposure} /></td><td data-label="Fix first">{r.fix ? <a href={r.fix.href}>{r.fix.text}</a> : <span className="muted">-</span>}</td></tr>)}</tbody>
          </table></div>
        </PlainCard>
      ) : null}
      <section className="sec" id="components" aria-labelledby="bc-h">
        <h2 id="bc-h">By component</h2>
        {s.svc ? <p className="muted small">Showing the components that affect <b>{s.svc}</b>. <a href="/vulns#components">Show all</a></p> : null}
        <form className="filters vfilter" method="get" action="/vulns#components" aria-label="Filter components">
          <Pick name="env" label="Env class" value={s.env ?? ""} options={d.choices.env} />
          <Pick name="pri" label="Priority" value={s.pri ?? ""} options={d.choices.pri} />
          <Pick name="kind" label="Kind" value={s.kind ?? ""} options={d.choices.kind} />
          <Pick name="svc" label="Service" value={s.svc ?? ""} options={d.choices.svc} />
          <label className={s.host ? "f-pill f-host on" : "f-pill f-host"}><span className="f-l">Host</span><input name="host" defaultValue={s.host} maxLength={80} autoComplete="off" /></label>
          <label className={s.kev ? "f-pill on" : "f-pill"}><input type="checkbox" name="kev" value="1" defaultChecked={s.kev === "1"} /><span>KEV only</span></label>
          <label className={s.state ? "f-pill on" : "f-pill"}><input type="checkbox" name="state" value="suppressed" defaultChecked={s.state === "suppressed"} /><span>Suppressed</span></label>
          <Button variant="outline">Apply</Button>
          {d.filtered ? <a data-slot="button" className={buttonVariants({ variant: "ghost", size: "icon" })} href="/vulns#components" title="Reset the filters" aria-label="Reset the filters"><Icon name="reset" /></a> : null}
          <span className="sr" role="status">{d.total} components</span>
        </form>
        <PlainCard as="section" className="l-card" aria-label="Components">
          <GroupTable rows={d.rows} label="Components table" empty={<>{d.empty}{d.filtered ? <> <a href="/vulns">Clear the filters</a></> : null}</>} />
        </PlainCard>
        {d.more ? <p className="l-note"><a href={d.more.href}>{d.more.text}</a></p> : null}
      </section>
      <Coverage c={d.coverage} />
    </>
  );
}

function Group({ d, csrf, flash }: { d: VulnGroup; csrf: string; flash: Banner | null }) {
  return (
    <>
      <header className="phead"><div><Crumb href="/vulns" label="Vulnerabilities" /><h1>{d.name}</h1>
        <p className="meta-line"><Pill tone={tone(d.priKind)} title="priority">{d.pri || "?"}</Pill><span>{d.kind} · {d.ecosystem}</span><span>{d.class}</span><b>{d.open} open</b><span>{d.targetCount} targets</span><span>{d.affects}</span></p></div></header>
      <Banners list={d.banners} flash={flash} />
      <div className="stack">
        <Summary l={d.summary} />
        {d.problem ? <ProblemBox p={d.problem} csrf={csrf} /> : null}
        <PlainCard as="section" className="l-card" aria-labelledby="vu-h">
          <div className="card-h rows-h"><h2 id="vu-h">Vulnerabilities <span className="ncount">{d.vulns.length}</span></h2></div>
          <div className="tablewrap" role="region" aria-label="Vulnerabilities table" tabIndex={0}><table className="l-table">
            <thead><tr><th scope="col">Priority</th><th scope="col">Vulnerability</th><th scope="col">Severity</th><th scope="col">Fixed in</th><th scope="col">SLA due</th></tr></thead>
            <tbody>{d.vulns.map((v) => (
              <tr key={v.id}>
                <td className="nw"><Pill tone={tone(v.priKind)} title="priority">{v.pri || "?"}</Pill> <span className="muted small">{v.score}</span></td>
                <td className="t-main">{v.href ? <a href={v.href} rel="noopener noreferrer">{v.id}</a> : <b>{v.id}</b>}{v.kev ? <> <Pill tone="fail">KEV</Pill></> : null}{v.suppressed ? <> <Pill>suppressed</Pill></> : null}
                  <span className="sub" dir="auto">{v.title}</span><span className="sub">why: {v.why}</span><span className="sub"><a href="#affected">{v.affects}</a></span></td>
                <td data-label="Severity" className="nw">{v.severity}</td><td data-label="Fixed in" className="nw mono">{v.fixed}</td><td data-label="SLA due" className="nw">{v.due}</td>
              </tr>
            ))}</tbody>
          </table></div>
          {d.more ? <p className="pad small"><a href={d.more.href}>{d.more.text}</a></p> : null}
        </PlainCard>
        <PlainCard as="section" className="l-card" id="affected" aria-labelledby="wa-h">
          <div className="card-h rows-h"><h2 id="wa-h">Who is affected <span className="ncount">{d.affects}</span></h2></div>
          {d.affected.length ? <>
            <p className="pad muted small">Each target mapped to the services that load the vulnerable code: a unit whose binary or interpreter comes from the package, or links its library; the services built from a lockfile or a base image; the whole host for the kernel and the OS release. Restart these after the upgrade (the handoff lists them per host).</p>
            <details className="fold-t fold-c" open={d.affected.length <= 40}><summary>Hosts and services <span className="ncount">{d.affected.length}</span></summary><AffectedTable rows={d.affected} label="Hosts and services" /></details>
          </> : <p className="empty">Not mapped yet: the next scan maps each target to its services.</p>}
        </PlainCard>
        {d.fix ? <FixCards f={d.fix} /> : null}
        <PlainCard as="section" className="l-card" aria-labelledby="tg-h">
          <div className="card-h rows-h"><h2 id="tg-h">Targets <span className="ncount">{d.targetCount}</span></h2></div>
          <div className="tablewrap" role="region" aria-label="Targets table" tabIndex={0}><table className="l-table">
            <thead><tr><th scope="col">Target</th><th scope="col">Version</th><th scope="col">Env</th></tr></thead>
            <tbody>{d.targets.map((t) => <tr key={t.target}><td>{t.href ? <a href={t.href}>{t.target}</a> : t.target}</td><td data-label="Version" className="mono">{t.version}</td><td data-label="Env">{t.env ? <EnvPill env={t.env} /> : null}</td></tr>)}</tbody>
          </table></div>
        </PlainCard>
        <PlainCard as="section" className="l-card" id="suppressions" aria-labelledby="su-h">
          <div className="card-h rows-h"><h2 id="su-h">Suppressions <span className="ncount">{d.rules.length}</span></h2></div>
          {d.rules.length ? (
            <ul className="vrules">{d.rules.map((r) => (
              <li key={r.id}><b className="mono">{r.vuln}</b><span>on {r.component} {r.target} {r.env} until {r.expires} by {r.by}: <span dir="auto">{r.reason}</span></span>
                {d.canSuppress ? <form action={vulnSuppress}><Hidden csrf={csrf} action="remove" id={r.id} back={d.key} /><Button variant="outline" size="sm">Remove</Button></form> : null}</li>
            ))}</ul>
          ) : <p className="empty">None for this component.</p>}
          {d.suppress ? (
            <details className="fold-t fold-c"><summary>Suppress</summary>
              <form action={vulnSuppress} className="stack-f pad">
                <Hidden csrf={csrf} action="add" back={d.key} />
                <label className="field"><span>Vulnerability <small>id or glob, empty = all of this component</small></span><input name="vuln_key" defaultValue={d.suppress.vuln} maxLength={120} autoComplete="off" /></label>
                <label className="field"><span>Component</span><input name="component" defaultValue={d.suppress.component} maxLength={120} autoComplete="off" /></label>
                <label className="field"><span>Target <small>glob, optional</small></span><input name="target" maxLength={120} autoComplete="off" /></label>
                <label className="field"><span>Env class <small>optional</small></span><input name="env" defaultValue={d.suppress.env} maxLength={120} autoComplete="off" /></label>
                <label className="field"><span>Reason <small>at least 20 characters</small></span><input name="reason" required minLength={20} maxLength={500} autoComplete="off" /></label>
                <label className="field"><span>Expires <small>at most 180 days</small></span><input name="expires" type="date" defaultValue={d.suppress.until} required /></label>
                <p><Button variant="outline">Suppress</Button></p>
              </form>
            </details>
          ) : null}
        </PlainCard>
      </div>
    </>
  );
}

export default async function VulnsPage({ searchParams }: { searchParams: Search }) {
  const s = await searchParams;
  const [csrf, flash] = await Promise.all([csrfToken(), readFlash()]);
  if (one(s.group) || one(s.id)) {
    const params = await pick(Promise.resolve(s), ["group", "id", "sent", "preq", "pq", "all"]);
    const r = await apiGet<VulnGroup>("/api/v1/vulns/group" + qs(params));
    if (r.status === 404) notFound();
    if (r.status !== 200) throw new Error(`/api/v1/vulns/group answered ${r.status}`);
    return <Group d={r.data} csrf={csrf} flash={flash} />;
  }
  const params = await pick(Promise.resolve(s), ["env", "pri", "kind", "svc", "host", "kev", "eol", "state", "all"]);
  const r = await apiGet<Vulns>("/api/v1/vulns" + qs(params));
  if (r.status !== 200) throw new Error(`/api/v1/vulns answered ${r.status}`);
  return <List d={r.data} flash={flash} />;
}

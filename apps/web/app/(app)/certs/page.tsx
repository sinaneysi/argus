import { HelpQ } from "@/components/HelpQ";
import type { Metadata } from "next";
import { At, Bar, P, Reload, Scan } from "@/components/Infra";
import { Markdown } from "@/components/Markdown";
import { Banners } from "@/components/Section";
import { apiGet, csrfToken, readFlash } from "@/lib/server";
import type { CertRow, Certs } from "@/lib/types";
import { PlainCard } from "@/components/ui/card";

export const metadata: Metadata = { title: "Certificates" };

function Table({ rows, label }: { rows: CertRow[]; label: string }) {
  return (
    <div className="tablewrap" role="region" aria-label={label} tabIndex={0}><table className="l-table">
      <thead><tr><th scope="col">Status</th><th scope="col">Certificate</th><th scope="col">Validity (of 90 days)</th><th scope="col" className="num">Days left</th><th scope="col">Expires</th><th scope="col">Used by</th></tr></thead>
      <tbody>{rows.map((c, i) => (
        <tr key={i}>
          <td><P p={c.status} /></td><td className="t-main"><span className="mono">{c.label}</span><span className="sub">{c.issuer}</span></td>
          <td data-label="Validity"><span className="meter"><Bar pct={c.pct} kind={c.level} /></span></td>
          <td data-label="Days left" className="num nw">{c.days} d</td><td data-label="Expires" className="nw muted">{c.expires}</td>
          <td data-label="Used by"><details><summary>{c.names.length} name{c.names.length === 1 ? "" : "s"}{c.mismatches ? ` · ${c.mismatches} SAN mismatch` : ""}</summary>
            <ul className="list-plain small">{c.names.map((n) => <li key={n.name} className="mono">{n.name}{n.mismatch ? <b> (SAN mismatch)</b> : null}</li>)}</ul></details></td>
        </tr>
      ))}</tbody>
    </table></div>
  );
}

export default async function CertsPage() {
  const [r, csrf, flash] = await Promise.all([apiGet<Certs>("/api/v1/certs"), csrfToken(), readFlash()]);
  if (r.status !== 200) throw new Error(`/api/v1/certs answered ${r.status}`);
  const d = r.data;
  const hot = d.hot ?? [];
  const ok = d.ok ?? [];
  return (
    <>
      <Reload r={d.refresh} />
      <header className="phead"><div><h1>Certificates</h1><HelpQ p="/certs" />
        {d.ran ? <p className="meta-line">{d.status ? <P p={d.status} /> : null}{d.names !== undefined ? <><span>{d.names} names</span><span>{d.certificates} certificates</span><span>{d.unreachable?.length ?? 0} unreachable</span></> : <span>{d.hosts?.length ?? 0} hosts</span>}<span>checked <At ts={d.at ?? 0} /></span></p> : null}</div>
        <div className="phead-act"><Scan s={d.scan} csrf={csrf} /></div></header>
      <Banners list={d.banners} flash={flash} />
      {!d.ran ? <p className="empty"><b>No runs yet.</b> No cert-check run recorded yet.</p> : (
        <>
          {d.summary ? <PlainCard as="details" className="fold-t"><summary>Agent summary</summary><div className="md"><Markdown text={d.summary} /></div></PlainCard> : null}
          {d.hosts ? (
            <PlainCard as="section" className="l-card" aria-label="Certificates"><div className="tablewrap" role="region" aria-label="Certificates table" tabIndex={0}><table className="l-table">
              <thead><tr><th scope="col">Status</th><th scope="col">Host</th><th scope="col">Validity (of 90 days)</th><th scope="col" className="num">Days left</th><th scope="col">Expires</th><th scope="col">Issuer</th></tr></thead>
              <tbody>{d.hosts.map((h) => <tr key={h.host}><td><P p={h.status} /></td><td className="t-main mono">{h.host}</td><td data-label="Validity"><span className="meter"><Bar pct={h.pct} kind={h.level} /></span></td><td data-label="Days left" className="num nw">{h.days} d</td><td data-label="Expires" className="nw muted">{h.expires}</td><td data-label="Issuer">{h.issuer}</td></tr>)}</tbody>
            </table></div>{!d.hosts.length ? <p className="empty">No certificates checked.</p> : null}</PlainCard>
          ) : (
            <>
              <PlainCard as="section" className="l-card" aria-labelledby="ce-h"><div className="card-h rows-h"><h2 id="ce-h">By certificate</h2></div>
                {hot.length ? <Table rows={hot} label="Certificates that need a look" /> : !ok.length ? <p className="empty">No certificate in the last check.</p> : <p className="empty">Every live certificate is OK.</p>}
                {ok.length ? <details open className="fold-t fold-c"><summary>Certificates OK <span className="ncount">{ok.length}</span></summary><Table rows={ok} label="Certificates OK" /></details> : null}
                {d.stale?.length ? <details className="fold-t fold-c"><summary>Expired long ago <span className="ncount">{d.stale.length}</span></summary><p className="pad muted small">Expired weeks ago: a stale vhost or a forgotten certificate. Listed every day, never paged.</p><Table rows={d.stale} label="Expired certificates" /></details> : null}
                {d.unreachable?.length ? <details open className="fold-t fold-c"><summary>Unreachable <span className="ncount">{d.unreachable.length}</span></summary><p className="pad muted small">No TLS answer from {d.host} (DNS, timeout, refused). Not certificate failures.</p>
                  <div className="tablewrap" role="region" aria-label="Unreachable names" tabIndex={0}><table className="l-table"><thead><tr><th scope="col">Name</th><th scope="col">Reason</th></tr></thead><tbody>{d.unreachable.map((u) => <tr key={u.label}><td className="t-main mono">{u.label}</td><td data-label="Reason" className="muted">{u.value}</td></tr>)}</tbody></table></div></details> : null}
              </PlainCard>
            </>
          )}
        </>
      )}
    </>
  );
}

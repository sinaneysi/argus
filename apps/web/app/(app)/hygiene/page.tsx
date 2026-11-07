import { HelpQ } from "@/components/HelpQ";
import type { Metadata } from "next";
import { At, Reload, Scan } from "@/components/Infra";
import { Pill } from "@/components/Pills";
import { Banners, Tiles } from "@/components/Section";
import { apiGet, csrfToken, readFlash } from "@/lib/server";
import type { Hygiene } from "@/lib/types";
import { PlainCard } from "@/components/ui/card";

export const metadata: Metadata = { title: "Alert hygiene" };

const SHOW = 5;
type Row = Hygiene["sections"][number]["rows"][number];

function Rows({ rows, when }: { rows: Row[]; when: string }) {
  return (
    <div className="tablewrap" role="region" aria-label="Alerts" tabIndex={0}><table className="l-table">
      <thead><tr><th scope="col">Alert</th><th scope="col">DC</th><th scope="col">Instance</th><th scope="col">{when}</th></tr></thead>
      <tbody>{rows.map((f, i) => (
        <tr key={i}><td className="t-main"><b>{f.alert}</b> {f.severity ? <span className="muted small">{f.severity}</span> : null}{f.why ? <span className="sub">{f.why}</span> : null}</td>
          <td data-label="DC"><Pill>{f.dc}</Pill></td><td data-label="Instance" className="mono">{f.instance}</td>
          <td data-label={when} className="nw">{f.when}{f.neverResolved ? <> <Pill tone="fail">never resolved</Pill></> : null}{f.detail ? <span className="sub">{f.detail}</span> : null}</td></tr>
      ))}</tbody>
    </table></div>
  );
}

export default async function HygienePage() {
  const [r, csrf, flash] = await Promise.all([apiGet<Hygiene>("/api/v1/hygiene"), csrfToken(), readFlash()]);
  if (r.status !== 200) throw new Error(`/api/v1/hygiene answered ${r.status}`);
  const d = r.data;
  return (
    <>
      <Reload r={d.refresh} />
      <header className="phead"><div><h1>Alert hygiene</h1><HelpQ p="/hygiene" />
        {d.loaded ? <p className="status-line">report <At ts={d.at} /><span className="sep">·</span>counters: {d.counters}</p> : null}</div>
        <div className="phead-act"><Scan s={d.scan} csrf={csrf} /></div></header>
      <Banners list={d.banners} flash={flash} />
      <Tiles tiles={d.tiles} />
      <p className="l-note">Weekly in the Saturday brief, refreshed daily. Suggestions only: Argus changes no rule; a change goes to the monitoring repository as a merge request reviewed by a human.</p>
      {d.sections.map((s) => (
        <PlainCard as="section" key={s.title} className="l-card" aria-label={s.title}>
          <div className="card-h rows-h"><h2>{s.title} <span className="ncount">{s.rows.length}</span></h2></div>
          <p className="pad muted small">{s.note}{s.suggestion ? <><br /><b>Suggested change:</b> {s.suggestion}</> : null}</p>
          <Rows rows={s.rows.slice(0, SHOW)} when={s.whenLabel} />
          {s.rows.length > SHOW ? <details className="fold-t fold-c"><summary>{s.rows.length - SHOW} more alerts</summary><Rows rows={s.rows.slice(SHOW)} when={s.whenLabel} /></details> : null}
        </PlainCard>
      ))}
      {d.loaded && !d.sections.length ? <p className="empty"><b>Nothing to clean up.</b> No stale, test, dead, flapping or noisy alert found.</p> : null}
    </>
  );
}

import { HelpQ } from "@/components/HelpQ";
import type { Metadata } from "next";
import { notFound } from "next/navigation";
import { Pill } from "@/components/Pills";
import { Crumb, pick, Report, type Search } from "@/components/Section";
import { Card, CardContent } from "@/components/ui/card";
import { qs } from "@/lib/api";
import { full } from "@/lib/format";
import { apiGet } from "@/lib/server";
import type { IncidentRun } from "@/lib/types";
import { notConfigured } from "@/components/NotConfigured";

export const metadata: Metadata = { title: "Incident run" };

export default async function RunPage({ searchParams }: { searchParams: Search }) {
  const nc = await notConfigured("/incidents/run", "Incident run");
  if (nc) return nc;
  const r = await apiGet<IncidentRun>("/api/v1/incidents/run" + qs(await pick(searchParams, ["id"])));
  if (r.status === 404) notFound();
  if (r.status !== 200) throw new Error(`/api/v1/incidents/run answered ${r.status}`);
  const d = r.data;
  return (
    <>
      <header className="phead"><div><Crumb href="/incidents" label="Incidents" /><h1>log-rca · {full(d.at)}</h1><HelpQ p="/incidents" /><p className="meta-line"><Pill tone={d.status === "OK" ? "ok" : "warn"}>{d.status}</Pill></p></div></header>
      <div className="stack">
        <Card as="section"><CardContent><Report doc={d.md} /></CardContent></Card>
        <Card as="section" className="l-card block" aria-labelledby="al-h">
          <div className="card-h pad"><h2 id="al-h">Alerts firing at that time</h2></div>
          {d.alerts.length ? (
            <div className="tablewrap"><table className="l-table"><thead><tr><th>Since</th><th>Severity</th><th>Alert</th><th>Target</th></tr></thead>
              <tbody>{d.alerts.map((a, i) => <tr key={i}><td className="nw muted">{a.since}</td><td>{a.severity}</td><td>{a.name}</td><td className="mono">{a.target}</td></tr>)}</tbody></table></div>
          ) : <p className="empty">None recorded.</p>}
        </Card>
      </div>
    </>
  );
}

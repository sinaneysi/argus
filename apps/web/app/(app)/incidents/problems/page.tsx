import { HelpQ } from "@/components/HelpQ";
import type { Metadata } from "next";
import { EnvPill, Pill } from "@/components/Pills";
import { Crumb, tone } from "@/components/Section";
import { Card, TitledCard } from "@/components/ui/card";
import { full } from "@/lib/format";
import { apiGet } from "@/lib/server";
import type { Findings } from "@/lib/types";

export const metadata: Metadata = { title: "Findings" };

export default async function FindingsPage() {
  const r = await apiGet<Findings>("/api/v1/incidents/findings");
  if (r.status !== 200) throw new Error(`/api/v1/incidents/findings answered ${r.status}`);
  const d = r.data;
  return (
    <>
      <header className="phead"><div><Crumb href="/incidents" label="Incidents" /><h1>{d.word}</h1><HelpQ p="/incidents" /><p className="psub">{d.word} found by chat triage, merged RCA reports and log-rca runs</p></div></header>
      <div className="stack">
        <Card as="section" className="l-card block" aria-labelledby="f-h">
          <div className="card-h pad"><h2 id="f-h">Chat triage <span className="ncount">{d.findings.length}</span></h2></div>
          <p className="card-sub pad">From the {d.room} room and Uptime Kuma.</p>
          {d.findings.length ? (
            <div className="tablewrap"><table className="l-table"><thead><tr><th>Found</th><th>Severity</th><th>Env</th><th>Finding</th></tr></thead>
              <tbody>{d.findings.map((f) => (
                <tr key={f.href}><td className="nw muted">{full(f.at)}</td><td><Pill tone={tone(f.sevKind)}>{f.severity}</Pill></td><td>{f.env ? <EnvPill env={f.env} /> : null}</td>
                  <td className="t-main"><a href={f.href} dir="auto">{f.title}</a>{f.suspicious ? <> <Pill tone="fail">suspicious</Pill></> : null}</td></tr>
              ))}</tbody></table></div>
          ) : <p className="empty">Nothing found yet.</p>}
        </Card>
        <TitledCard titleId="m-h" title={<>RCA reports (merged) <span className="ncount">{d.files.length}</span></>}>
          <p className="card-sub">All reports, including the ones not merged yet: <a href="/rca">RCA library</a>.</p>
          {d.files.length ? <ul className="plain">{d.files.map((f) => <li key={f.href}><a href={f.href}>{f.text}</a></li>)}</ul> : <p className="empty">None merged yet.</p>}
        </TitledCard>
        <Card as="section" className="l-card block" aria-labelledby="r-h">
          <div className="card-h pad"><h2 id="r-h">log-rca runs <span className="ncount">{d.runs.length}</span></h2></div>
          {d.runs.length ? (
            <div className="tablewrap"><table className="l-table"><thead><tr><th>Run</th><th>Status</th><th>Summary</th><th><span className="sr">Merge request</span></th></tr></thead>
              <tbody>{d.runs.map((x) => (
                <tr key={x.href}><td className="nw muted">{full(x.at)}</td><td><Pill tone={x.status === "OK" ? "ok" : "warn"}>{x.status}</Pill></td><td className="t-main"><a href={x.href} dir="auto">{x.summary}</a></td>
                  <td>{x.mr ? <a href={x.mr} rel="noopener noreferrer">MR</a> : null}</td></tr>
              ))}</tbody></table></div>
          ) : <p className="empty">No log-rca run recorded yet.</p>}
        </Card>
      </div>
    </>
  );
}

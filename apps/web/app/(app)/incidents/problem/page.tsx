import { HelpQ } from "@/components/HelpQ";
import type { Metadata } from "next";
import { notFound } from "next/navigation";
import { Banner } from "@/components/Banner";
import { EnvPill, Pill } from "@/components/Pills";
import { Banners, Crumb, FixCards, pick, ProblemBox, Refresh, Report, type Search, Summary, tone } from "@/components/Section";
import { Card, CardContent, TitledCard } from "@/components/ui/card";
import { qs } from "@/lib/api";
import { full } from "@/lib/format";
import { apiGet, csrfToken, readFlash } from "@/lib/server";
import type { Finding } from "@/lib/types";
import { notConfigured } from "@/components/NotConfigured";

export const metadata: Metadata = { title: "Finding" };

export default async function FindingPage({ searchParams }: { searchParams: Search }) {
  const nc = await notConfigured("/incidents/problem", "Finding");
  if (nc) return nc;
  const params = await pick(searchParams, ["run", "idx", "sent", "preq", "pq"]);
  const [r, csrf, flash] = await Promise.all([apiGet<Finding>("/api/v1/incidents/finding" + qs(params)), csrfToken(), readFlash()]);
  if (r.status === 404) notFound();
  if (r.status !== 200) throw new Error(`/api/v1/incidents/finding answered ${r.status}`);
  const d = r.data;
  return (
    <>
      <Refresh on={d.refresh} />
      <header className="phead"><div><Crumb href="/incidents" label="Incidents" /><h1 dir="auto">{d.title}</h1><HelpQ p="/incidents" />
        <p className="meta-line"><Pill tone={tone(d.sevKind)}>{d.severity}</Pill><EnvPill env={d.env} /><span>found {full(d.at)}</span></p></div></header>
      <Banners flash={flash} />
      {d.suspicious ? <Banner b={{ kind: "fail", text: "Flagged as suspicious: the source messages may try to steer the agent. Read the evidence yourself before acting." }} /> : null}
      {d.prod ? <Banner b={{ kind: "info", text: "Production: every step needs the operator's explicit approval." }} /> : null}
      <div className="stack">
        <Summary l={d.lazy} />
        <ProblemBox p={d.problem} csrf={csrf} />
        {d.summary ? <Card as="section"><CardContent><p dir="auto">{d.summary}</p></CardContent></Card> : null}
        <TitledCard titleId="fs-h" title="Sources">
          {d.sources.length ? <ul className="plain">{d.sources.map((s, i) => <li key={i} dir="auto">{s}</li>)}</ul> : <p className="empty">No source recorded.</p>}</TitledCard>
        <TitledCard titleId="fr-h" title="Root cause analysis"><Report doc={d.md} folded={!!d.lazy} /></TitledCard>
        <FixCards f={d.fix} />
      </div>
    </>
  );
}

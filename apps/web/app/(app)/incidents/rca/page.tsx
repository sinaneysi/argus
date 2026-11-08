import { HelpQ } from "@/components/HelpQ";
import type { Metadata } from "next";
import { notFound } from "next/navigation";
import { Crumb, FixCards, Jump, pick, Rail, Report, type Search, Summary } from "@/components/Section";
import { Card, CardContent } from "@/components/ui/card";
import { qs } from "@/lib/api";
import { apiGet } from "@/lib/server";
import type { RcaFile } from "@/lib/types";
import { notConfigured } from "@/components/NotConfigured";

export const metadata: Metadata = { title: "RCA report" };

export default async function MergedReport({ searchParams }: { searchParams: Search }) {
  const nc = await notConfigured("/incidents/rca", "RCA report");
  if (nc) return nc;
  const r = await apiGet<RcaFile>("/api/v1/incidents/report" + qs(await pick(searchParams, ["f"])));
  if (r.status === 404) notFound();
  if (r.status !== 200) throw new Error(`/api/v1/incidents/report answered ${r.status}`);
  const d = r.data;
  return (
    <>
      <header className="phead"><div><Crumb href="/rca" label="RCA library" /><h1 dir="auto">{d.title}</h1><HelpQ p="/incidents" /><p className="psub">Merged RCA report</p></div></header>
      <Jump items={d.outline} />
      <div className="detail">
        <div className="stack">
          <Summary l={d.lazy} />
          <Card as="section"><CardContent><Report doc={d.md} folded={!!d.lazy} ids /></CardContent></Card>
          {d.fix ? <FixCards f={d.fix} /> : null}
        </div>
        <aside><Rail title="Report" facts={[{ label: "File", value: d.file, mono: true }, { label: "State", value: "merged" }]} /></aside>
      </div>
    </>
  );
}

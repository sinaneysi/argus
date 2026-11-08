import { HelpQ } from "@/components/HelpQ";
import type { Metadata } from "next";
import { notFound } from "next/navigation";
import { Pill } from "@/components/Pills";
import { Banners, Crumb, FixCards, Jump, pick, ProblemBox, Rail, Refresh, Report, type Search, Summary, tone } from "@/components/Section";
import { qs } from "@/lib/api";
import { full } from "@/lib/format";
import { apiGet, csrfToken, readFlash } from "@/lib/server";
import type { RcaItem } from "@/lib/types";
import { buttonVariants } from "@/components/ui/button";
import { PlainCard } from "@/components/ui/card";
import { notConfigured } from "@/components/NotConfigured";

export const metadata: Metadata = { title: "RCA report" };

export default async function RcaView({ searchParams }: { searchParams: Search }) {
  const nc = await notConfigured("/rca/view", "RCA report");
  if (nc) return nc;
  const params = await pick(searchParams, ["id", "sent", "preq", "pq"]);
  const [r, csrf, flash] = await Promise.all([apiGet<RcaItem>("/api/v1/rca/item" + qs(params)), csrfToken(), readFlash()]);
  if (r.status === 404) notFound();
  if (r.status !== 200) throw new Error(`/api/v1/rca/item answered ${r.status}`);
  const d = r.data;
  return (
    <>
      <Refresh on={d.refresh} />
      <header className="phead"><div><Crumb href="/rca" label="RCA library" /><h1 dir="auto">{d.title}</h1><HelpQ p="/rca" />
        <p className="meta-line"><Pill tone={tone(d.statusKind)}>{d.status}</Pill><span>{full(d.at)}</span>{d.incident ? <a href={`/incidents/view?id=${d.incident}`}>incident #{d.incident}</a> : null}</p></div></header>
      <Banners list={d.banners} flash={flash} />
      <Jump items={d.outline} />
      <div className="detail">
        <div className="stack">
          <Summary l={d.lazy} />
          <ProblemBox p={d.problem} csrf={csrf} />
          <PlainCard as="section"><Report doc={d.md} folded={!!d.lazy} ids /></PlainCard>
          <FixCards f={d.fix} />
          {d.tasks ? (
            <PlainCard as="section" id="fix-tasks" aria-labelledby="ft-h">
              <div className="card-h"><h2 id="ft-h">{d.tasks.title} <span className="ncount">{d.tasks.items.length}</span></h2></div>
              <p className="card-sub">Each one becomes a task on the board with its evidence and prompt. Production fixes stay proposals for a human.</p>
              <ul className="plain">{d.tasks.items.map((t) => <li key={t.n}><b>{t.n}.</b> <span dir="auto">{t.title}</span> {t.task.href ? <a data-slot="button" className={buttonVariants({ variant: "outline", size: "sm" })} href={t.task.href}>{t.task.text}</a> : null}</li>)}</ul>
            </PlainCard>
          ) : null}
        </div>
        <aside><Rail title="Report" facts={d.facts} /></aside>
      </div>
    </>
  );
}

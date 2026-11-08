import { HelpQ } from "@/components/HelpQ";
import type { Metadata } from "next";
import { notFound } from "next/navigation";
import { TaskLink } from "@/components/Delivery";
import { P } from "@/components/Infra";
import { Pill } from "@/components/Pills";
import { Banners, Crumb, pick, ProblemBox, Rail, type Search, Segs } from "@/components/Section";
import { qs } from "@/lib/api";
import { apiGet, csrfToken, readFlash } from "@/lib/server";
import type { CicdItem } from "@/lib/types";
import { PlainCard } from "@/components/ui/card";
import { notConfigured } from "@/components/NotConfigured";

export const metadata: Metadata = { title: "CI/CD finding" };

export default async function FindingPage({ searchParams }: { searchParams: Search }) {
  const nc = await notConfigured("/cicd/finding", "CI/CD finding");
  if (nc) return nc;
  const params = await pick(searchParams, ["id", "sent", "preq", "pq", "all"]);
  const [r, csrf, flash] = await Promise.all([apiGet<CicdItem>("/api/v1/cicd/finding" + qs(params)), csrfToken(), readFlash()]);
  if (r.status === 404) notFound();
  if (r.status !== 200) throw new Error(`/api/v1/cicd/finding answered ${r.status}`);
  const d = r.data;
  const links = d.links.flatMap((l, i) => (i ? [{ text: " · " }, l] : [l]));
  return (
    <>
      <header className="phead"><div><Crumb href="/cicd" label="CI/CD" /><h1 dir="auto">{d.title}</h1><HelpQ p="/cicd" /><p className="meta-line"><P p={d.status} /></p></div><div className="phead-act"><TaskLink t={d.task} /></div></header>
      <Banners flash={flash} list={d.prod ? [{ text: "Production: every action here is a proposal for the operator. Argus never runs a production change.", kind: "info" }] : []} />
      <div className="detail">
        <div className="stack">
          {d.detail ? <PlainCard as="section"><p dir="auto">{d.detail}</p></PlainCard> : null}
          <PlainCard as="section" className="l-card" aria-labelledby="pf-h">
            <div className="card-h rows-h"><h2 id="pf-h">Proposed fix</h2></div>
            {d.proposals.length ? (
              <ul className="rows">{d.proposals.map((p, i) => (
                <li key={i}><p dir="auto"><b>{p.text}</b></p>
                  {p.call ? <p className="mono small">{p.call}</p> : null}
                  {p.curl && p.curl !== p.call ? <details className="fold"><summary>curl</summary><pre className="code"><code>{p.curl}</code></pre></details> : null}
                  {p.rule ? <p className="muted small">{p.rule}</p> : null}
                  {p.needsToken ? <p><Pill title="CICD_WRITE=0: no write token yet">Proposed: needs write token</Pill></p> : null}</li>
              ))}</ul>
            ) : <p className="empty">No proposal.</p>}
            <p className="pad muted small">Writes: {d.writes}. The exact API call is shown so the operator can run it, or so Argus can once a write token exists and the operator&apos;s rule allows it.</p>
          </PlainCard>
          {d.problem ? <ProblemBox p={d.problem} csrf={csrf} /> : null}
          <PlainCard as="section" aria-labelledby="ev-h"><h2 id="ev-h" className="h-card">Evidence</h2><pre className="code prompt" role="region" aria-label="Evidence text" tabIndex={0}><code>{d.evidence}</code></pre></PlainCard>
        </div>
        <aside><Rail facts={d.rail} />{d.links.length ? <PlainCard as="section" className="rail-c"><h2>Links</h2><p className="small"><Segs s={links} /></p></PlainCard> : null}</aside>
      </div>
    </>
  );
}

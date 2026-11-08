import { HelpQ } from "@/components/HelpQ";
import type { Metadata } from "next";
import { notFound } from "next/navigation";
import { CicdTabs, Cell, Findings } from "@/components/Delivery";
import { P } from "@/components/Infra";
import { EnvPill } from "@/components/Pills";
import { Banners, Crumb, one, Rail, type Search, When } from "@/components/Section";
import { qs } from "@/lib/api";
import { apiGet, readFlash } from "@/lib/server";
import type { CicdProject } from "@/lib/types";
import { PlainCard } from "@/components/ui/card";
import { notConfigured } from "@/components/NotConfigured";

export const metadata: Metadata = { title: "CI/CD project" };

export default async function ProjectPage({ searchParams }: { searchParams: Search }) {
  const nc = await notConfigured("/cicd/project", "CI/CD project");
  if (nc) return nc;
  const s = await searchParams;
  const [r, flash] = await Promise.all([apiGet<CicdProject>("/api/v1/cicd/project" + qs({ id: one(s.id) })), readFlash()]);
  if (r.status === 404) notFound();
  if (r.status !== 200) throw new Error(`/api/v1/cicd/project answered ${r.status}`);
  const d = r.data;
  return (
    <>
      <header className="phead"><div><Crumb href="/cicd" label="CI/CD" /><h1>{d.name}</h1><HelpQ p="/cicd" /><p className="meta-line"><span>{d.findings.length} open findings</span></p></div></header>
      <Banners flash={flash} />
      <CicdTabs />
      <PlainCard as="section" className="l-card" aria-label="Env branches"><div className="tablewrap" role="region" aria-label="Env branches table" tabIndex={0}><table className="l-table">
        <thead><tr>{d.cells.map((c) => <th key={c.env} scope="col">{c.env}</th>)}</tr></thead><tbody><tr>{d.cells.map((c) => <td key={c.env} data-label={c.env}><Cell c={c} /></td>)}</tr></tbody>
      </table></div></PlainCard>
      <div className="detail">
        <div className="stack">
          <PlainCard as="section" className="l-card" aria-labelledby="of-h">
            <div className="card-h rows-h"><h2 id="of-h">Open findings <span className="ncount">{d.findings.length}</span></h2></div>
            {d.findings.length ? <Findings rows={d.findings} project={false} /> : <p className="empty"><b>No open finding.</b> Nothing failing, stuck or drifting for this project.</p>}
          </PlainCard>
          <PlainCard as="section" className="l-card" aria-labelledby="de-h">
            <div className="card-h rows-h"><h2 id="de-h">Deploys</h2></div>
            {d.deploys.length ? (
              <div className="tablewrap" role="region" aria-label="Deploys table" tabIndex={0}><table className="l-table">
                <thead><tr><th scope="col">When</th><th scope="col">Env</th><th scope="col">sha</th><th scope="col">Author</th><th scope="col">Hosts</th><th scope="col">Verified</th></tr></thead>
                <tbody>{d.deploys.map((x, i) => <tr key={i}><td className="nw"><When ts={x.at} /></td><td data-label="Env">{x.env ? <EnvPill env={x.env} /> : null}</td><td data-label="sha" className="mono">{x.sha}</td><td data-label="Author">{x.author}</td><td data-label="Hosts" className="mono">{x.hosts}</td><td data-label="Verified"><P p={x.verified} /></td></tr>)}</tbody>
              </table></div>
            ) : <p className="empty">No deploy recorded in the last 14 days.</p>}
          </PlainCard>
          <PlainCard as="section" className="l-card" aria-label="Pipelines">
            <details className="fold-t fold-c"><summary>Pipelines <span className="ncount">{d.pipelines.length}</span></summary>
              {d.pipelines.length ? (
                <div className="tablewrap" role="region" aria-label="Pipelines table" tabIndex={0}><table className="l-table">
                  <thead><tr><th scope="col" className="num">Pipeline</th><th scope="col">Env</th><th scope="col">Status</th><th scope="col">sha</th><th scope="col">Created</th><th scope="col">Deploy bridge</th></tr></thead>
                  <tbody>{d.pipelines.map((x) => <tr key={x.id}><td className="num mono">{x.id}</td><td data-label="Env">{x.env ? <EnvPill env={x.env} /> : null}</td><td data-label="Status"><P p={x.status} /></td><td data-label="sha" className="mono">{x.sha}</td><td data-label="Created" className="nw"><When ts={x.at} /></td><td data-label="Deploy bridge" className="muted">{x.bridge}</td></tr>)}</tbody>
                </table></div>
              ) : <p className="empty">No pipeline on an env branch.</p>}
            </details>
          </PlainCard>
        </div>
        <aside><Rail facts={d.rail} /></aside>
      </div>
    </>
  );
}

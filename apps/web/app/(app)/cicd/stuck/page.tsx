import { HelpQ } from "@/components/HelpQ";
import type { Metadata } from "next";
import { CicdTabs, Findings } from "@/components/Delivery";
import { Banners, Crumb, When } from "@/components/Section";
import { apiGet, readFlash } from "@/lib/server";
import type { CicdStuck } from "@/lib/types";
import { PlainCard } from "@/components/ui/card";
import { notConfigured } from "@/components/NotConfigured";

export const metadata: Metadata = { title: "Stuck pipelines" };

export default async function StuckPage() {
  const nc = await notConfigured("/cicd/stuck", "Stuck pipelines");
  if (nc) return nc;
  const [r, flash] = await Promise.all([apiGet<CicdStuck>("/api/v1/cicd/stuck"), readFlash()]);
  if (r.status !== 200) throw new Error(`/api/v1/cicd/stuck answered ${r.status}`);
  const d = r.data;
  return (
    <>
      <header className="phead"><div><Crumb href="/cicd" label="CI/CD" /><h1>Stuck pipelines</h1><HelpQ p="/cicd" /><p className="meta-line"><b>{d.rows.length} stuck</b></p></div></header>
      <Banners flash={flash} />
      <CicdTabs on="/cicd/stuck" />
      <PlainCard as="section" className="l-card" aria-labelledby="st-h">
        <div className="card-h rows-h"><h2 id="st-h">Waiting for the deploy lock or a runner <span className="ncount">{d.rows.length}</span></h2></div>
        {d.rows.length ? <Findings rows={d.rows} /> : <p className="empty"><b>No stuck pipeline.</b> Nothing has waited for the deploy lock or a runner for more than {d.minutes} minutes.</p>}
      </PlainCard>
      <PlainCard as="section" className="l-card" aria-labelledby="wm-h">
        <div className="card-h rows-h"><h2 id="wm-h">Release deploys waiting at manual <span className="ncount">{d.waiting.length}</span></h2></div>
        {d.waiting.length ? <Findings rows={d.waiting} /> : <p className="empty">None found by the last audit.</p>}
      </PlainCard>
      <PlainCard as="section" className="l-card" aria-labelledby="rr-h">
        <div className="card-h rows-h"><h2 id="rr-h">Release pipelines still running <span className="ncount">{d.running.length}</span></h2></div>
        <p className="pad muted small">A release pipeline whose prod deploy (project {d.engineId}) waits at manual keeps its project&apos;s trigger-deploy lock. It may be cancelled only while it holds the lock and its prod deploy is still manual; Argus proposes that in the stuck finding when another pipeline waits behind it. Never played by Argus.</p>
        <details className="fold-t fold-c"><summary>Release pipelines running for more than 1 h <span className="ncount">{d.running.length}</span></summary>
          {d.running.length ? (
            <div className="tablewrap" role="region" aria-label="Release pipelines" tabIndex={0}><table className="l-table">
              <thead><tr><th scope="col">Project</th><th scope="col" className="num">Pipeline</th><th scope="col">sha</th><th scope="col">Started</th></tr></thead>
              <tbody>{d.running.map((x) => <tr key={x.id}><td><a href={`/cicd/project?id=${x.projectId}`}>{x.project}</a></td><td data-label="Pipeline" className="num mono">{x.id}</td><td data-label="sha" className="mono">{x.sha}</td><td data-label="Started" className="nw"><When ts={x.at} /></td></tr>)}</tbody>
            </table></div>
          ) : <p className="empty">None.</p>}
        </details>
      </PlainCard>
    </>
  );
}

import { HelpQ } from "@/components/HelpQ";
import type { Metadata } from "next";
import { CicdTabs, Cell, EnvCardView, Findings, RepoBox } from "@/components/Delivery";
import { Icon } from "@/components/Icon";
import { P } from "@/components/Infra";
import { Banners, Filters, Pager, pick, type Search, Tiles } from "@/components/Section";
import { qs } from "@/lib/api";
import { apiGet, readFlash } from "@/lib/server";
import type { Cicd } from "@/lib/types";
import { PlainCard } from "@/components/ui/card";
import { SoftLink } from "@/components/Soft";

export const metadata: Metadata = { title: "CI/CD" };

export default async function CicdPage({ searchParams }: { searchParams: Search }) {
  const params = await pick(searchParams, ["view", "group", "kind", "env", "state", "page"]);
  const [r, flash] = await Promise.all([apiGet<Cicd>("/api/v1/cicd" + qs(params)), readFlash()]);
  if (r.status !== 200) throw new Error(`/api/v1/cicd answered ${r.status}`);
  const d = r.data;
  return (
    <>
      <header className="phead"><div><h1>CI/CD</h1><HelpQ p="/cicd" /><p className="meta-line"><span><b>{d.failing} failing pipelines</b> ({d.onRelease} on release)</span><span>{d.stuck} stuck pipelines</span></p></div></header>
      <Banners list={d.banners} flash={flash} />
      <CicdTabs on="/cicd" />
      {d.attention.length ? (
        <PlainCard as="section" className="l-card" aria-labelledby="at-h">
          <div className="card-h rows-h"><h2 id="at-h">Needs attention <span className="ncount">{d.attention.length < d.attentionTotal ? `${d.attention.length} of ${d.attentionTotal} findings` : `${d.attentionTotal} findings`}</span></h2><a className="small" href="/cicd/failures">All failures</a></div>
          <Findings rows={d.attention} />
        </PlainCard>
      ) : <PlainCard as="section"><p className="empty"><b>Nothing failing or stuck.</b> No open CI/CD failure on develop, pre-release or release, and no pipeline waiting for the deploy lock.</p></PlainCard>}
      <section className="sec" aria-labelledby="now-h"><h2 id="now-h">Now</h2><Tiles tiles={d.tiles} /></section>
      {d.envs ? (
        <section className="sec" aria-labelledby="env-h">
          <div className="card-h env-h"><h2 id="env-h">Green by environment</h2>
            <nav className="tabs" aria-label="Count apps or packages">{d.kindLinks.map((k, i) => { const on = (i === 0) === (d.envs?.kind === "app"); return <SoftLink key={k.text} href={k.href} className={on ? "on" : undefined} aria-current={on ? "true" : undefined}>{k.text}</SoftLink>; })}</nav></div>
          <p className="muted small">{d.envs.count} {d.envs.noun} that include {d.envs.engine}: the newest pipeline that ran on each env branch since {d.envs.since} ({d.envs.days} days; [skip ci] pipelines ignored). Every figure opens the matrix filtered to those {d.envs.noun}.</p>
          <div className="envs">{d.envs.cards.map((c) => <EnvCardView key={c.env} c={c} noun={d.envs?.noun ?? "repos"} />)}</div>
        </section>
      ) : null}
      <RepoBox placeholder={d.placeholder} />
      <section className="sec" id="matrix" aria-labelledby="mx-h">
        <h2 id="mx-h">Estate</h2>
        <p className="muted small">Last pipeline per env branch (status, sha, age) and the last deploy. Tags: drift means the branch has commits its lower env never had, stale means no pipeline in 30 days, stuck means waiting for the deploy lock. Healthy projects are hidden until you choose All.</p>
        {d.pick ? <div className="banner warn" role="status"><Icon name="warn" /><span>Showing the {d.pick.count} {d.pick.noun} whose newest {d.pick.env} pipeline is: <b>{d.pick.state}</b>. <a href={d.pick.clear}>Clear the filter</a></span></div> : null}
        <Filters list={d.list} facets={d.facets} label="Filter the estate" placeholder="" />
        {d.readiness ? <p className="muted small">{d.readiness} <a href="/cicd/repo">Check a repo</a></p> : null}
        <PlainCard as="section" className="l-card" aria-label="Estate">
          {d.rows.length ? (
            <div className="tablewrap" role="region" aria-label="Estate table" tabIndex={0}><table className="l-table">
              <thead><tr><th scope="col">Project</th><th scope="col">Readiness</th><th scope="col">develop</th><th scope="col">pre-release</th><th scope="col">release</th></tr></thead>
              <tbody>{d.rows.map((x) => (
                <tr key={x.id}>
                  <td className="t-main"><a href={`/cicd/project?id=${x.id}`}>{x.name}</a><span className="sub">{x.executor}{x.include ? ` · include ${x.include}` : ""}</span></td>
                  <td data-label="Readiness"><a href={x.readiness.href}>{x.readiness.pill ? <P p={x.readiness.pill} /> : <span className="muted small">not checked</span>}</a></td>
                  {x.cells.map((c) => <td key={c.env} data-label={c.env}><Cell c={c} /></td>)}
                </tr>
              ))}</tbody>
            </table></div>
          ) : <p className="empty">{d.pick ? "No repo in this state." : "No project in this view. Choose All to see every project."}</p>}
        </PlainCard>
        <Pager list={d.list} />
      </section>
      <p className="l-note">Read-only: Argus reads GitLab as Reporter and proposes fixes; it never cancels, retries, plays or triggers a pipeline and never changes a variable. Writes: {d.writes}.</p>
    </>
  );
}

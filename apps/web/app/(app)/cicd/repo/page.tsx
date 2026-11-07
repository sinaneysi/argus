import { HelpQ } from "@/components/HelpQ";
import type { Metadata } from "next";
import { RepoBox, RepoCheckCtl } from "@/components/Delivery";
import { P, Reload } from "@/components/Infra";
import { EnvPill } from "@/components/Pills";
import { Banners, Crumb, Filters, FixCards, Pager, pick, Rail, type Search, Tiles, When } from "@/components/Section";
import { qs } from "@/lib/api";
import { apiGet, csrfToken, readFlash } from "@/lib/server";
import type { Banner, RepoCheckRow, RepoIndex, RepoPage } from "@/lib/types";
import { PlainCard } from "@/components/ui/card";

export const metadata: Metadata = { title: "Check a repo" };

function Checks({ rows, label }: { rows: RepoCheckRow[]; label: string }) {
  return (
    <div className="tablewrap" role="region" aria-label={label} tabIndex={0}><table className="l-table">
      <thead><tr><th scope="col">Status</th><th scope="col">Check</th><th scope="col">Evidence and fix</th></tr></thead>
      <tbody>{rows.map((r) => (
        <tr key={r.id}>
          <td><P p={r.status} /></td>
          <td className="t-main"><b>{r.title}</b><span className="sub mono">{r.id} · {r.severity}</span></td>
          <td data-label="Evidence">{r.evidence}{r.where ? <span className="sub mono">{r.where}</span> : null}
            {r.fix ? <details className="fold"><summary>Fix{r.fix.file ? <>: <span className="mono">{r.fix.file}</span></> : null}</summary><pre className="code"><code>{r.fix.text}</code></pre><p className="muted small">{r.fix.why} Source: {r.fix.source}</p></details> : null}</td>
        </tr>
      ))}</tbody>
    </table></div>
  );
}

function Index({ d, flash }: { d: RepoIndex; flash: Banner | null }) {
  return (
    <>
      <header className="phead"><div><Crumb href="/cicd" label="CI/CD" /><h1>Check a repo</h1><HelpQ p="/cicd" /><p className="psub">Is a repo ready for the shared CI/CD pipeline?</p></div></header>
      <Banners list={d.banners} flash={flash} />
      <RepoBox value={d.value} error={d.error} placeholder={d.placeholder} />
      {d.checked ? <div className="sec"><Tiles tiles={d.tiles} /></div> : null}
      <section className="sec" id="repos" aria-labelledby="cr-h">
        <h2 id="cr-h">Checked repos <span className="ncount">{d.checked}</span></h2>
        {d.checked ? <Filters list={d.list} facets={d.facets} label="Search checked repos" placeholder="Project path" /> : null}
        <PlainCard as="section" className="l-card" aria-label="Checked repos">
          {d.rows.length ? (
            <div className="tablewrap" role="region" aria-label="Checked repos table" tabIndex={0}><table className="l-table">
              <thead><tr><th scope="col">Project</th><th scope="col">Readiness</th><th scope="col" className="num">Blockers</th><th scope="col" className="num">Warnings</th><th scope="col">Checked</th><th scope="col">By</th></tr></thead>
              <tbody>{d.rows.map((r) => <tr key={r.href}><td className="t-main"><a className="mono" href={r.href}>{r.project}</a></td><td data-label="Readiness"><P p={r.verdict} /></td><td data-label="Blockers" className="num">{r.blockers}</td><td data-label="Warnings" className="num">{r.warnings}</td><td data-label="Checked" className="nw"><When ts={r.at} /></td><td data-label="By" className="muted">{r.by}</td></tr>)}</tbody>
            </table></div>
          ) : d.checked ? <p className="empty">No checked repo matches these filters. <a href={d.list.clearHref}>Clear the filters</a></p> : <p className="empty">No repo checked yet. Paste a project link above.</p>}
        </PlainCard>
        <Pager list={d.list} />
      </section>
      <p className="l-note">What is checked: the include of {d.engine}, the env branches and their protection, package.json scripts, the lockfile (committed, in sync, version, registry), the Node version, .env.sample, the Dockerfile choice, npm package rules and, when ci-vars-meta has a token, the CI variables (keys only). The rules are config/cicd-requirements.json. Read-only: Argus never changes the repo.</p>
    </>
  );
}

function Repo({ d, csrf, flash }: { d: RepoPage; csrf: string; flash: Banner | null }) {
  const rep = d.report;
  return (
    <>
      <Reload r={d.refresh} />
      <header className="phead"><div><Crumb href="/cicd/repo" label="Check a repo" /><h1>{d.name}</h1><HelpQ p="/cicd" /><p className="meta-line">{rep ? <P p={rep.verdict} /> : null}<span className="mono">{d.path}</span></p></div>
        <div className="phead-act"><RepoCheckCtl c={d.check} target={d.target} csrf={csrf} /></div></header>
      <Banners list={d.banners} flash={flash} />
      <RepoBox value={d.path} placeholder={d.placeholder} />
      {!rep ? <PlainCard as="section" className="l-card"><p className="empty"><b>Not checked yet.</b> Argus has no report for this project. &quot;Check this repo&quot; reads it from GitLab (read-only) and shows what the pipeline needs.</p></PlainCard> : (
        <>
          <PlainCard as="section" className="verdict" aria-label="Verdict">
            <p><P p={rep.verdict} /><span className="muted small">{rep.blockers} blockers · {rep.warnings} warnings · {rep.passed} passed</span></p>
            {rep.summary.length ? <ul>{rep.summary.map((s, i) => <li key={i}>{s}</li>)}</ul> : null}
            {rep.error ? <p className="muted small">Checked <When ts={rep.checkedAt} />: {rep.error}</p> : null}
          </PlainCard>
          {rep.error ? null : (
            <div className="detail">
              <div className="stack">
                <PlainCard as="section" className="l-card" aria-labelledby="ck-h">
                  <div className="card-h rows-h"><h2 id="ck-h">Checks <span className="ncount">{rep.open?.length ?? 0}</span></h2></div>
                  {rep.open?.length ? <Checks rows={rep.open} label="Open checks" /> : <p className="empty">Every applicable check passed.</p>}
                  {rep.rest?.length ? <details className="fold-t fold-c"><summary>Passed and not applicable <span className="ncount">{rep.rest.length}</span></summary><Checks rows={rep.rest} label="Passed checks" /></details> : null}
                </PlainCard>
                <PlainCard as="section" className="l-card" aria-labelledby="pe-h">
                  <div className="card-h rows-h"><h2 id="pe-h">Pipelines per environment</h2></div>
                  <div className="tablewrap" role="region" aria-label="Pipelines per environment table" tabIndex={0}><table className="l-table">
                    <thead><tr><th scope="col">Env</th><th scope="col">Branch</th><th scope="col">Newest pipeline</th><th scope="col">sha</th><th scope="col">When</th></tr></thead>
                    <tbody>{(rep.pipelines ?? []).map((p) => (
                      <tr key={p.env}><td><EnvPill env={p.env} /></td>
                        {p.branch ? <>
                          <td data-label="Branch">{p.protected ? "protected" : <b className="hot-fail">not protected</b>}{p.devPush ? <b className="hot-warn"> · Developers can push</b> : null}</td>
                          <td data-label="Pipeline">{p.status ? <P p={p.status} /> : <span className="muted small">no pipeline</span>}{p.extra ? <span className="sub">{p.extra}</span> : null}</td>
                          <td data-label="sha" className="mono">{p.sha}</td><td data-label="When" className="nw"><When ts={p.at ?? 0} /></td>
                        </> : <><td data-label="Branch" className="muted">no branch</td><td data-label="Pipeline">-</td><td data-label="sha">-</td><td data-label="When">-</td></>}
                      </tr>
                    ))}</tbody>
                  </table></div>
                  <p className="pad muted small">Pipelines {rep.pipelinesFrom}. Read-only: Argus never pushes, never opens the MR itself and never changes a CI variable.</p>
                </PlainCard>
                {rep.fix ? <FixCards f={rep.fix} /> : null}
              </div>
              <aside><Rail facts={rep.rail ?? []} /></aside>
            </div>
          )}
        </>
      )}
    </>
  );
}

export default async function RepoCheckPage({ searchParams }: { searchParams: Search }) {
  const params = await pick(searchParams, ["project", "sent", "q", "verdict", "sort", "page"]);
  const [r, csrf, flash] = await Promise.all([apiGet<RepoIndex | RepoPage>("/api/v1/cicd/repo" + qs(params)), csrfToken(), readFlash()]);
  if (r.status !== 200) throw new Error(`/api/v1/cicd/repo answered ${r.status}`);
  return r.data.mode === "index" ? <Index d={r.data} flash={flash} /> : <Repo d={r.data} csrf={csrf} flash={flash} />;
}

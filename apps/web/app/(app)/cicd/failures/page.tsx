import { HelpQ } from "@/components/HelpQ";
import type { Metadata } from "next";
import { CicdTabs, TaskLink } from "@/components/Delivery";
import { EnvPill, Pill } from "@/components/Pills";
import { Banners, Crumb, Filters, Pager, pick, type Search, When } from "@/components/Section";
import { qs } from "@/lib/api";
import { apiGet, readFlash } from "@/lib/server";
import type { CicdFailures, CicdFinding } from "@/lib/types";
import { PlainCard } from "@/components/ui/card";
import { SoftLink } from "@/components/Soft";
import { notConfigured } from "@/components/NotConfigured";

export const metadata: Metadata = { title: "CI/CD failures" };

const SEV: Record<string, "fail" | "warn"> = { critical: "fail", high: "fail", medium: "warn" };

function Row({ f }: { f: CicdFinding }) {
  const open = f.status === "open";
  return (
    <li className="fr" id={`f-${f.id}`}>
      <div className="fr-b">
        <p className="th">{f.severity ? <Pill tone={SEV[f.severity] ?? "neutral"}>{f.severity}</Pill> : <Pill title="No severity recorded">unrated</Pill>}
          <a href={`/cicd/finding?id=${f.id}`} dir="auto">{f.title}</a>
          <span className="muted small">{open ? <When ts={f.lastSeen} /> : <>resolved <When ts={f.resolvedAt} /></>}</span></p>
        <p className="fr-m"><span className="kind">{f.klass}</span>
          {f.projectId ? <a className="mono" href={`/cicd/project?id=${f.projectId}`}>{f.project}</a> : f.project ? <span className="mono">{f.project}</span> : null}
          {f.env ? <EnvPill env={f.env} /> : null}{f.job ? <span title="Job">{f.job}</span> : null}
          {f.occurrences > 1 ? <span>{f.occurrences}× since <When ts={f.firstSeen} /></span> : null}
          {f.author ? <span>{f.author}{f.mr ? ` ${f.mr}` : ""}</span> : null}</p>
        {f.detail ? <p className="fr-x" dir="auto" title={f.detail}>{f.detail}</p> : null}
        {f.proposed ? <p className="fr-x" title={f.proposed}>Proposed: {f.proposed}</p> : null}
        {!open && f.resolveNote ? <p className="fr-x" dir="auto">Resolved: {f.resolveNote}</p> : null}
      </div>
      {open ? <div className="fr-e"><TaskLink t={f.task} /></div> : null}
    </li>
  );
}

export default async function FailuresPage({ searchParams }: { searchParams: Search }) {
  const nc = await notConfigured("/cicd/failures", "CI/CD failures");
  if (nc) return nc;
  const params = await pick(searchParams, ["view", "q", "env", "class", "sort", "page"]);
  const [r, flash] = await Promise.all([apiGet<CicdFailures>("/api/v1/cicd/failures" + qs(params)), readFlash()]);
  if (r.status !== 200) throw new Error(`/api/v1/cicd/failures answered ${r.status}`);
  const d = r.data;
  const openView = d.view === "open";
  return (
    <>
      <header className="phead"><div><Crumb href="/cicd" label="CI/CD" /><h1>CI/CD failures</h1><HelpQ p="/cicd" />
        <p className="meta-line"><b>{d.open ? `${d.open} open failure${d.open === 1 ? "" : "s"}` : "No open failure"}</b>{d.onRelease ? <a href="/cicd/failures?env=release">{d.onRelease} on release</a> : null}<span>Argus never retries, cancels or plays a pipeline</span></p></div></header>
      <Banners flash={flash} />
      <CicdTabs on="/cicd/failures" />
      <nav className="tabs" aria-label="Failure views">{d.tabs.map((t) => <SoftLink key={t.label} href={t.href} className={t.on ? "on" : undefined} aria-current={t.on ? "page" : undefined}>{t.label} <span className="num">{t.count}</span></SoftLink>)}</nav>
      {d.noTools ? null : <Filters list={d.list} facets={d.facets} label="Search and filter failures" placeholder="Title, project, job, author, MR, commit, branch" />}
      <PlainCard as="section" className="l-card" aria-label={openView ? "Open failures" : "Resolved failures"}>
        {d.rows.length ? <ul className="rows">{d.rows.map((f) => <Row key={f.id} f={f} />)}</ul>
          : d.list.filtered ? <p className="empty">No failure matches these filters. <a href={d.list.clearHref}>Clear the filters</a></p>
          : openView ? <p className="empty"><b>No open failure.</b> Every env branch passed its last pipeline, or the failures were fixed.</p>
          : <p className="empty"><b>Nothing resolved yet.</b> A failure resolves itself when the next pipeline on the same branch passes.</p>}
      </PlainCard>
      <Pager list={d.list} />
      <p className="l-note">Classified by rules first, a small model only for traces no rule knows (at most 10 a day). A failure resolves itself when the next pipeline on the same branch passes; one that passed on retry becomes flaky.</p>
    </>
  );
}

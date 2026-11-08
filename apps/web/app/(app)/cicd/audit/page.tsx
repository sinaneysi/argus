import { HelpQ } from "@/components/HelpQ";
import type { Metadata } from "next";
import { CicdTabs, Findings } from "@/components/Delivery";
import { Banners, Crumb, When } from "@/components/Section";
import { apiGet, readFlash } from "@/lib/server";
import type { CicdAudit } from "@/lib/types";
import { PlainCard } from "@/components/ui/card";
import { notConfigured } from "@/components/NotConfigured";

export const metadata: Metadata = { title: "Branches and variables" };

export default async function AuditPage() {
  const nc = await notConfigured("/cicd/audit", "Branches and variables");
  if (nc) return nc;
  const [r, flash] = await Promise.all([apiGet<CicdAudit>("/api/v1/cicd/audit"), readFlash()]);
  if (r.status !== 200) throw new Error(`/api/v1/cicd/audit answered ${r.status}`);
  const d = r.data;
  return (
    <>
      <header className="phead"><div><Crumb href="/cicd" label="CI/CD" /><h1>Branches and variables</h1><HelpQ p="/cicd" />
        <p className="meta-line"><b>{d.branchFindings} branch findings</b><span>variables: {d.mode}</span>{d.auditedAt ? <span>audited <When ts={d.auditedAt} /></span> : null}</p></div></header>
      <Banners flash={flash} />
      <CicdTabs on="/cicd/audit" />
      {d.secCount ? (
        <PlainCard as="section" className="l-card" id="security" aria-labelledby="se-h">
          <div className="card-h rows-h"><h2 id="se-h">Security and configuration <span className="ncount">{d.secCount}</span></h2></div>
          <p className="pad muted small">From the estate survey (variable flags, protection, routing, the engine); static until ci-vars-meta can verify them daily. Every fix is the operator&apos;s.</p>
          {d.secTop.length ? <Findings rows={d.secTop} /> : null}
          {d.secRest.length ? <details className="fold-t fold-c"><summary>Medium and low <span className="ncount">{d.secRest.length}</span></summary><Findings rows={d.secRest} /></details> : null}
        </PlainCard>
      ) : null}
      {d.sections.map((s) => (
        <PlainCard as="section" key={s.rule} className="l-card" aria-labelledby={`r-${s.rule}`}>
          <div className="card-h rows-h"><h2 id={`r-${s.rule}`}>{s.title} <span className="ncount">{s.count}</span></h2></div>
          {s.lead.length ? <Findings rows={s.lead} /> : null}
          {s.more.length ? <details className="fold-t fold-c"><summary>{s.lead.length ? "More findings" : "Findings"} <span className="ncount">{s.more.length}</span> <span className="muted small">low or informational</span></summary><Findings rows={s.more} /></details> : null}
        </PlainCard>
      ))}
      {!d.sections.length ? <PlainCard as="section" className="l-card">{d.auditedAt ? <p className="empty"><b>No branch finding.</b> Every env branch exists, is protected and contains only what its lower env had.</p> : <p className="empty"><b>Not audited yet.</b> The branch audit runs daily; findings appear after its first run.</p>}</PlainCard> : null}
      <PlainCard as="section" className="l-card" id="variables" aria-labelledby="va-h">
        <div className="card-h rows-h"><h2 id="va-h">Variable audit <span className="ncount">{d.variables.length}</span></h2></div>
        {d.varsNote ? <p className="pad muted small">{d.varsNote}</p> : null}
        {d.variables.length ? <Findings rows={d.variables} /> : d.auditedAt ? <p className="empty"><b>No variable problem found.</b> Nothing in the job logs points at a missing or wrong variable.</p> : <p className="empty"><b>Not audited yet.</b> The variable audit runs with the branch audit.</p>}
      </PlainCard>
      <p className="l-note">Branch audit daily: env branches exist; release has no commit that skipped pre-release and pre-release none that skipped develop (merge and hotfix commits excluded); stale env branches; the default branch; the {d.engine} include ref; protection of release and pre-release. Variables: keys only, never values.</p>
    </>
  );
}

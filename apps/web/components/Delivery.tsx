import "./delivery.css";
import { P } from "./Infra";
import { Icon } from "./Icon";
import { EnvPill, Pill } from "./Pills";
import { Hidden, tone, When } from "./Section";
import { repoCheck } from "@/app/(app)/cicd/actions";
import type { CicdCell, CicdFinding, EnvCard, RepoPage, Seg, VulnAffected, VulnExposure } from "@/lib/types";
import { Button, buttonVariants } from "@/components/ui/button";
import { PlainCard } from "@/components/ui/card";

// The parts the Delivery pages share. Server components: forms post through server actions and the bars are SVG
// attributes, so everything works without client JavaScript under the strict CSP.

const TABS = [["Overview", "/cicd"], ["Failures", "/cicd/failures"], ["Stuck", "/cicd/stuck"], ["Branches and variables", "/cicd/audit"]] as const;

export function CicdTabs({ on }: { on?: string }) {
  return <nav className="tabs" aria-label="CI/CD sections">{TABS.map(([l, h]) => <a key={h} href={h} className={h === on ? "on" : undefined} aria-current={h === on ? "page" : undefined}>{l}</a>)}</nav>;
}

export function TaskLink({ t }: { t: Seg }) {
  return t.href ? <a data-slot="button" className={buttonVariants({ variant: "outline", size: "sm" })} href={t.href}><Icon name="list" />{t.text}</a> : null;
}

/** One open finding: what, where, what Argus proposes, and its task. */
export function FindingRow({ f, project = true }: { f: CicdFinding; project?: boolean }) {
  return (
    <li className="fr">
      <div className="fr-b">
        <p className="th"><Pill tone={tone(f.kind)}>{f.klass}</Pill><a href={`/cicd/finding?id=${f.id}`} dir="auto">{f.title}</a></p>
        <p className="fr-m">
          {f.env ? <EnvPill env={f.env} /> : null}<When ts={f.lastSeen} />{f.occurrences > 1 ? <span className="num">{f.occurrences}×</span> : null}
          {project && f.projectId ? <a className="mono" href={`/cicd/project?id=${f.projectId}`}>{f.project}</a> : null}
          {f.author ? <span>{f.author}{f.mr ? ` ${f.mr}` : ""}</span> : null}
        </p>
        {f.detail && f.detail.replace(/\.$/, "") !== f.title.replace(/\.$/, "") ? <p className="fr-x" dir="auto" title={f.detail}>{f.detail}</p> : null}
        {f.proposed ? <p className="fr-x" title={f.proposed}>Proposed: {f.proposed}</p> : null}
      </div>
      <div className="fr-e"><TaskLink t={f.task} /></div>
    </li>
  );
}

export function Findings({ rows, project }: { rows: CicdFinding[]; project?: boolean }) {
  return <ul className="rows">{rows.map((f) => <FindingRow key={f.id} f={f} project={project} />)}</ul>;
}

/** Newest pipeline and deploy of one env branch. */
export function Cell({ c }: { c: CicdCell }) {
  if (!c.branch) return <span className="muted small">no branch</span>;
  return (
    <div className="cell">
      <p>{c.status ? <P p={c.status} /> : <span className="muted small">no pipeline 14 d</span>}{(c.tags ?? []).map((t) => <Pill key={t}>{t}</Pill>)}</p>
      <p className="muted small">
        {c.sha ? <><span className="mono">{c.sha}</span> <When ts={c.at ?? 0} /></> : null}
        {c.deployedAt ? <>{c.sha ? " · " : null}deployed <When ts={c.deployedAt} /></> : null}
        {c.regression ? <b className="hot-fail"> · regression</b> : null}
      </p>
    </div>
  );
}

export function EnvCardView({ c, noun }: { c: EnvCard; noun: string }) {
  return (
    <PlainCard as="section" className="envc" aria-label={c.env}>
      <p className="envc-k"><EnvPill env={c.env} /><span className="muted small">{c.branches} with the branch</span></p>
      <a className="envc-big" href={c.href}><b className="num">{c.success}</b> <span className="muted">of {c.branches} {noun} green</span></a>
      <svg className="envbar" viewBox="0 0 100 8" preserveAspectRatio="none" role="img" aria-label={c.barLabel || "No pipelines"}>
        <rect width="100" height="8" className="track" />
        {c.bar.map((s) => <rect key={s.state} x={s.x.toFixed(2)} width={s.w.toFixed(2)} height="8" className={`s-${s.state}`} />)}
      </svg>
      <ul className="envrows">
        {c.rows.map((r) => (
          <li key={r.state}><a href={r.href}><i className={`dot s-${r.state}`} aria-hidden="true" /><span>{r.label}</span>{r.note ? <span className="muted small">{r.note}</span> : null}<b className={r.hot ? "num hot-fail" : "num"}>{r.count}</b></a></li>
        ))}
      </ul>
    </PlainCard>
  );
}

/** The paste box of Check a repo: a plain GET form. */
export function RepoBox({ value = "", error = "", placeholder }: { value?: string; error?: string; placeholder: string }) {
  return (
    <PlainCard as="form" className="repobox" method="get" action="/cicd/repo" role="search" aria-label="Check a repo">
      <div className="repobox-r">
        <label className="field"><span>Check a repo <small>Paste a GitLab project link: Argus checks whether it has what the shared pipeline needs.</small></span>
          <input name="project" type="text" maxLength={300} required autoComplete="off" spellCheck={false} placeholder={placeholder} defaultValue={value} aria-invalid={error ? true : undefined} aria-describedby={error ? "repo-e" : undefined} /></label>
        <Button variant="outline">Check</Button>
      </div>
      {error ? <p className="hot-fail small" id="repo-e" role="alert">{error}</p> : null}
    </PlainCard>
  );
}

export function RepoCheckCtl({ c, target, csrf }: { c: RepoPage["check"]; target: string; csrf: string }) {
  return (
    <div className="scanctl">
      {c.msg ? <span className="muted small" role={c.busy ? "status" : undefined}>{c.msg}</span> : null}
      {c.can ? (
        <form action={repoCheck}><Hidden csrf={csrf} project={target} /><Button variant="outline" title="Read the repo again from GitLab (read-only, about 10 calls, no model)"><Icon name="reset" />{c.label}</Button></form>
      ) : <Button variant="outline" disabled><Icon name="reset" />{c.busy ? "Checking…" : c.label}</Button>}
    </div>
  );
}

export function Exposure({ e }: { e: VulnExposure }) {
  if (e.word === "internal") return <span className="muted small">internal</span>;
  return <Pill tone={e.hot ? "fail" : "neutral"} title={e.via || undefined}>{e.word}</Pill>;
}

export function AffectedTable({ rows, more, moreHref, label }: { rows: VulnAffected[]; more?: number; moreHref?: string; label: string }) {
  return (
    <div className="tablewrap" role="region" aria-label={label} tabIndex={0}><table className="l-table">
      <thead><tr><th scope="col">Host</th><th scope="col">Env</th><th scope="col">Service</th><th scope="col">Exposure</th><th scope="col">Version installed</th></tr></thead>
      <tbody>
        {rows.map((a, i) => (
          <tr key={i}>
            <td className="nw">{a.hostHref ? <a href={a.hostHref}>{a.host}</a> : a.host}</td>
            <td data-label="Env">{a.env ? <EnvPill env={a.env} /> : <span className="muted">-</span>}</td>
            <td data-label="Service">{a.serviceHref ? <a href={a.serviceHref}>{a.service}</a> : a.service}<span className="sub mono">{a.unit}</span></td>
            <td data-label="Exposure"><Exposure e={a.exposure} />{a.exposure.via ? <span className="sub">{a.exposure.via}</span> : null}</td>
            <td data-label="Version" className="mono">{a.version}<span className="sub">{a.how}</span></td>
          </tr>
        ))}
        {more && moreHref ? <tr><td colSpan={5}><a href={moreHref}>{more} more</a></td></tr> : null}
      </tbody>
    </table></div>
  );
}


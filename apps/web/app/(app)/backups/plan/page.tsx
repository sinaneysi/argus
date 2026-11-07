import { HelpQ } from "@/components/HelpQ";
import type { Metadata } from "next";
import { notFound } from "next/navigation";
import { backupInstall, backupRestore } from "../../inventory/actions";
import { P, Reload, RestoreText } from "@/components/Infra";
import { Markdown } from "@/components/Markdown";
import { EnvPill, Pill } from "@/components/Pills";
import { Banners, Crumb, Hidden, one, Rail, type Search, When } from "@/components/Section";
import { qs } from "@/lib/api";
import { apiGet, csrfToken, readFlash } from "@/lib/server";
import type { BackupInstall, BackupPlan } from "@/lib/types";
import { Button, buttonVariants } from "@/components/ui/button";
import { PlainCard } from "@/components/ui/card";

export const metadata: Metadata = { title: "Backup plan" };

function Install({ i }: { i: BackupInstall }) {
  if (i.mode === "operator") {
    return (
      <PlainCard as="section" id="install" aria-labelledby="in-h"><div className="card-h"><h2 id="in-h">Install <span className="muted">the operator, after approving this change</span></h2></div>
        <p className="card-sub">One command on the operator&apos;s workstation. The bundle tests the connection first and installs nothing if it fails; then templates, units, the job, one backup now, the restore-list check, the timer.</p>
        <pre className="code prompt"><code>{i.command}</code></pre>
        <p className="muted small">Afterwards: <code>{i.after}</code> deletes {i.host}&apos;s copy of the connection string (Argus also deletes it when the first backup shows up on the target).</p>
        {i.installedAt ? <p className="small"><Pill tone="ok">installed</Pill> <When ts={i.installedAt} /></p> : null}</PlainCard>
    );
  }
  if (i.mode === "none") return <PlainCard as="section" id="install"><div className="card-h"><h2>Install</h2></div><p className="card-sub">Not installed yet. <b>Install now</b> runs the bundle on <span className="mono">{i.server}</span> through the fixer path: test, install, one backup now, verify, timer. The admin who clicks is the approver.</p></PlainCard>;
  if (i.mode === "running") return <PlainCard as="section" id="install"><div className="card-h"><h2>Installing</h2></div><p className="card-sub">Started by {i.by} <When ts={i.at} />. This page refreshes itself.</p></PlainCard>;
  return (
    <PlainCard as="section" id="install"><div className="card-h"><h2>Install <Pill tone={i.ok ? "ok" : "fail"}>{i.ok ? "installed" : "install failed"}</Pill></h2></div>
      <p className="card-sub">{i.by} · <When ts={i.at} />{i.secret ? ` · ${i.secret}` : ""}</p>
      {i.reason ? <p className="bk-reason">{i.reason}</p> : null}
      <ul className="steps">{i.steps.map((s, n) => <li key={n} className={s.kind}>{s.name ? <><b>{s.name}</b> {s.result} </> : null}<span className="muted">{s.detail}</span></li>)}</ul></PlainCard>
  );
}

export default async function BackupPlanPage({ searchParams }: { searchParams: Search }) {
  const sp = await searchParams;
  const [r, csrf, flash] = await Promise.all([apiGet<BackupPlan>("/api/v1/backups/plan" + qs({ id: one(sp.id), req: one(sp.req) })), csrfToken(), readFlash()]);
  if (r.status === 404) notFound();
  if (r.status !== 200) throw new Error(`/api/v1/backups/plan answered ${r.status}`);
  const d = r.data;
  const b = d.installBtn;
  return (
    <>
      <Reload r={d.refresh} />
      <header className="phead"><div><Crumb href="/backups#plans" label="Backups" /><h1>{d.name}</h1><HelpQ p="/backups" />
        <p className="meta-line"><P p={d.state} /><span>{d.engine}</span><EnvPill env={d.env} /><span className="mono">{d.server}</span><span>{d.schedule}</span></p></div></header>
      <Banners list={d.banners} flash={flash} />
      {d.prod ? <Banners list={[{ kind: "info", text: "Production change: needs the operator's explicit approval. This plan is a proposal: the operator approves this one change, merges the MR and installs it. Argus never installs it." }]} /> : null}
      <div className="actionbar">
        {b ? <form action={backupInstall}><Hidden csrf={csrf} plan={d.id} /><Button variant={b.again ? "outline" : "default"} disabled={b.busy}>{b.label}</Button></form> : null}
        {d.fixerHref ? <a data-slot="button" className={buttonVariants()} href={d.fixerHref}>Send to fixer</a> : null}
        {d.mrHref ? <a data-slot="button" className={buttonVariants({ variant: "outline" })} href={d.mrHref} rel="noopener noreferrer">Merge request</a> : null}
        {d.taskHref ? <a data-slot="button" className={buttonVariants({ variant: "outline" })} href={d.taskHref}>Board task</a> : null}
      </div>
      <div className="detail">
        <div className="stack">
          {d.warnings.length ? <PlainCard as="section"><ul className="steps">{d.warnings.map((w) => <li key={w} className="warn">{w}</li>)}</ul></PlainCard> : null}
          {d.overlaps.length ? <PlainCard as="section"><div className="card-h"><h2>Overlaps</h2></div><ul className="steps">{d.overlaps.map((w) => <li key={w} className="warn">{w}</li>)}</ul></PlainCard> : null}
          {d.install ? <Install i={d.install} /> : null}
          <PlainCard as="section" aria-labelledby="pl-h"><div className="card-h"><h2 id="pl-h">Plan</h2></div><div className="md"><Markdown text={d.plan} /></div></PlainCard>
          <PlainCard as="section" aria-labelledby="jd-h"><div className="card-h"><h2 id="jd-h">Job directory <span className="muted mono">{d.jobPath}</span></h2></div>
            {d.files.map((f) => <details key={f.path} className="fold-t"><summary><span className="mono">{f.path}</span>&nbsp;<span className="muted">{f.lines} lines</span></summary><pre className="code prompt"><code>{f.text}</code></pre></details>)}
            {d.fixPrompt ? <details className="fold-t"><summary>Fixer prompt&nbsp;<span className="muted">reviewed and sent from the board task by an admin</span></summary><pre className="code prompt"><code>{d.fixPrompt}</code></pre></details> : null}
          </PlainCard>
          <PlainCard as="section" id="restore" aria-labelledby="rt-h"><div className="card-h"><h2 id="rt-h">Restore tests</h2></div>
            {d.tests.length ? <div className="tablewrap" role="region" aria-label="Restore tests table" tabIndex={0}><table className="l-table"><thead><tr><th scope="col">Date</th><th scope="col">Result</th><th scope="col">Object</th><th scope="col">Notes</th><th scope="col">By</th></tr></thead>
              <tbody>{d.tests.map((t, i) => <tr key={i}><td className="t-main nw">{t.date}</td><td data-label="Result"><RestoreText r={t.result} /></td><td data-label="Object" className="mono">{t.object}</td><td data-label="Notes" dir="auto">{t.notes}</td><td data-label="By" className="muted nw">{t.by}</td></tr>)}</tbody></table></div>
              : <p className="empty">No restore test yet. Run the drill ({d.repo} docs/restore-drill.md) and record it here.</p>}
            {d.testForm ? (
              <form action={backupRestore} className="rt-form"><Hidden csrf={csrf} plan={d.id} />
                <label className="field"><span>Date</span><input name="date" defaultValue={d.testForm.today} maxLength={10} /></label>
                <label className="field"><span>Result</span><select name="result"><option value="ok">ok</option><option value="partial">partial</option><option value="failed">failed</option></select></label>
                <label className="field"><span>Object</span><input name="object" maxLength={300} placeholder="…/daily/2026/10/20/x.archive.zst.age" /></label>
                <label className="field"><span>Notes</span><input name="notes" maxLength={1000} placeholder="counts compared, time taken" /></label>
                <Button variant="outline">Record restore test</Button></form>
            ) : null}
          </PlainCard>
        </div>
        <aside><Rail facts={d.record} /></aside>
      </div>
    </>
  );
}

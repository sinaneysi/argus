import { HelpQ } from "@/components/HelpQ";
import type { Metadata } from "next";
import { backupNew } from "../../inventory/actions";
import { Reload } from "@/components/Infra";
import { Pill } from "@/components/Pills";
import { Banners, Crumb, one, Rail, type Search } from "@/components/Section";
import { qs } from "@/lib/api";
import { apiGet, csrfToken, readFlash } from "@/lib/server";
import type { BackupNew } from "@/lib/types";
import { Button, buttonVariants } from "@/components/ui/button";
import { PlainCard } from "@/components/ui/card";

export const metadata: Metadata = { title: "New backup" };

const KEYS = ["databases", "schedule", "cron", "target", "member", "name", "retention_days", "draft", "req"];

export default async function NewBackupPage({ searchParams }: { searchParams: Search }) {
  const sp = await searchParams;
  const params: Record<string, string> = {};
  for (const k of KEYS) if (one(sp[k])) params[k] = one(sp[k]);
  const [r, csrf, flash] = await Promise.all([apiGet<BackupNew>("/api/v1/backups/new" + qs(params)), csrfToken(), readFlash()]);
  if (r.status !== 200) throw new Error(`/api/v1/backups/new answered ${r.status}`);
  const d = r.data;
  const v = d.values;
  const t = d.test;
  const cur = v.schedule || "daily";
  const shown = d.schedules.find((s) => s.key === cur);
  const hasUri = !!d.current.uriRedacted;
  return (
    <>
      <Reload r={d.refresh} />
      <header className="phead"><div><Crumb href="/backups" label="Backups" /><h1>New backup</h1><HelpQ p="/backups" />
        <p className="status-line">Four fields<span className="sep">·</span>the job runs on the database host<span className="sep">·</span>tested before it is saved</p></div></header>
      <Banners list={d.banners} flash={flash} />
      {d.prod ? <Banners list={[{ kind: "info", text: "Production: Argus never connects to a production database and never installs there. Save opens a merge request and a board task; the operator runs the install bundle, which tests first." }]} /> : null}
      <div className="bk">
        <PlainCard as="form" action={backupNew} className="bk-form" autoComplete="off">
          <input type="hidden" name="csrf" value={csrf} />
          <div className="field"><span>Connection string</span>
            {hasUri ? <div className="bk-uri"><span className="mono">{d.current.uriRedacted}</span><a data-slot="button" className={buttonVariants({ variant: "outline", size: "sm" })} href={d.replace}>Replace</a>
              <input type="hidden" name="draft" value={d.draft} /><input type="hidden" name="sha" value={d.current.sha} /><input type="hidden" name="token" value={d.current.token} /></div>
              : <input type="password" name="uri" autoComplete="off" spellCheck={false} required={d.admin} aria-label="Connection string" placeholder="mongodb://user:password@host:27017/?replicaSet=rs0" />}
            <small>The scheme sets the engine: mongodb, mongodb+srv, mysql, mariadb, postgres, redis, sqlserver. Never shown again, never in the plan, the merge request, Jira or a log.</small></div>
          <label className="field"><span>Databases</span><input name="databases" defaultValue={v.databases} maxLength={200} placeholder="from the string, or all" /><small>Names separated by spaces, or <code>all</code>.</small></label>
          <fieldset><legend>Schedule</legend>
            <div className="bk-chips">{d.schedules.map((s) => <label key={s.key} className="check"><input type="radio" name="schedule" value={s.key} defaultChecked={s.key === cur} />{s.label}</label>)}</div>
            <div className="inline-f"><label className="field"><span className="sr">Cron expression</span><input name="cron" defaultValue={v.cron} maxLength={100} placeholder="cron: 15 1 * * *" /></label>
              <Button variant="outline" size="sm" name="action" value="preview" formNoValidate>Preview</Button></div>
            <p className="hint">{d.tz} time. The next five runs show on the right after Preview.</p></fieldset>
          <label className="field"><span>Target</span><select name="target" defaultValue={d.target}>{d.targets.map((o) => <option key={o.value} value={o.value}>{o.label}</option>)}</select>
            {d.noTarget ? <small>No backup target yet: an admin adds one once in <a href="/admin/backup-targets">Settings → Backup targets</a>; every user then picks it here.</small> : null}</label>
          <details className="fold-t"><summary>Advanced <span className="muted">derived from the string; change only if you must</span></summary>
            <dl className="rec wide">{d.derived.map((f) => <div key={f.label}><dt>{f.label}</dt><dd>{f.value}</dd></div>)}</dl>
            {d.members.length ? <fieldset className="picks"><legend>Runs on: a member of the {d.topology}; default a secondary</legend>
              {d.members.map((m) => <label key={m} className="check"><input type="radio" name="member" value={m} defaultChecked={m === d.member} /><span className="mono">{m}</span></label>)}</fieldset> : null}
            <div className="bk-row"><label className="field"><span>Job name <small>default: engine-db-host</small></span><input name="name" defaultValue={v.name} maxLength={41} pattern="[a-z0-9][a-z0-9-]{2,40}" /></label>
              <label className="field"><span>Retention, days</span><input name="retention_days" defaultValue={v.retention_days} inputMode="numeric" maxLength={3} placeholder="14" /></label></div>
          </details>
          <div className="bk-save">
            {!d.admin ? <span className="muted small">Admins test and save backups.</span>
              : d.tested ? <Button  name="action" value="save">{t.state === "deferred" ? "Save proposal" : "Save"}</Button>
                : t.state === "testing" ? <><Button  disabled>Testing…</Button><Button variant="outline" disabled title="Test the connection first">Save</Button></>
                  : <><Button  name="action" value="test">{t.state === "fail" ? "Test again" : "Test connection"}</Button><Button variant="outline" disabled title="Test the connection first">Save</Button></>}
            <span className="muted small">{d.note}</span>
          </div>
        </PlainCard>
        <aside className="stack" id="bk-rail" aria-live="polite">
          <PlainCard as="section" aria-labelledby="bt-h">
            {!t.state ? <><h2 id="bt-h" className="sec-h">Test connection</h2><p className="muted small">Runs on the database host itself (develop and pre-release, through the fixer path): authentication and a ping only, never a data read, 5 s timeout. It also reports the version, the role and any privilege the backup is missing.</p></>
              : t.state === "testing" ? <><h2 id="bt-h" className="sec-h">Testing</h2><p className="muted small">On <span className="mono">{t.member}</span>. This page refreshes itself.</p></>
                : t.state === "ok" ? <h2 id="bt-h" className="sec-h"><Pill tone="ok">passed</Pill> Connection works</h2>
                  : t.state === "deferred" ? <><h2 id="bt-h" className="sec-h"><Pill>production</Pill> Tested by the operator</h2><p className="muted small">Argus never connects to a production database. The operator&apos;s bundle tests first and installs nothing if it fails.</p></>
                    : <><h2 id="bt-h" className="sec-h"><Pill tone="fail">failed</Pill> Connection test</h2><p className="bk-reason">{t.reason}</p>{t.missing ? <><pre className="code"><code>{t.missing}</code></pre><p className="muted small">Run this as an admin of the database, then test again.</p></> : null}</>}
            {t.facts.length ? <dl className="rec wide">{t.facts.map((f) => <div key={f.label}><dt>{f.label}</dt><dd>{f.value}</dd></div>)}</dl> : null}
            {t.absent.length ? <p className="small hot-warn">Not on the server: {t.absent.join(", ")}</p> : null}
          </PlainCard>
          {shown ? <PlainCard as="section" aria-labelledby="br-h"><h2 id="br-h" className="sec-h">Next 5 runs <span className="muted small">{d.tz}</span></h2>
            {shown.runs.length ? <ol className="bk-runs mono">{shown.runs.map((x) => <li key={x}>{x}</li>)}</ol> : shown.error ? <p className="bk-reason">{shown.error}</p> : <p className="muted small">{shown.note}</p>}</PlainCard> : null}
          <Rail title="On Save" facts={[{ label: "Result", value: d.prod ? `A merge request in ${d.repo} and a board task marked PRODUCTION CHANGE; the operator runs the install bundle (it tests first).` : `The plan and a merge request in ${d.repo}. Then an admin clicks Install now: test, install, one backup now, verify, timer.` }]} />
        </aside>
      </div>
    </>
  );
}

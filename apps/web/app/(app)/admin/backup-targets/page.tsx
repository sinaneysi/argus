import type { Metadata } from "next";
import { AdminsOnly } from "@/components/Agent";
import { Banner } from "@/components/Banner";
import { Pill } from "@/components/Pills";
import { Banners, Hidden, pick, Refresh, tone, type Search } from "@/components/Section";
import { SetForm } from "@/components/SetForm";
import { SettingsTabs } from "@/components/Settings";
import { qs } from "@/lib/api";
import { plural } from "@/lib/format";
import { apiGet, csrfToken, readFlash } from "@/lib/server";
import type { AdminTargets } from "@/lib/types";
import { settingsGo } from "../settings";
import { Button } from "@/components/ui/button";
import { PlainCard } from "@/components/ui/card";

export const metadata: Metadata = { title: "Backup targets" };

const PAGE = "/admin/backup-targets";

function Row({ t, csrf, host }: { t: AdminTargets["rows"][number]; csrf: string; host: string }) {
  return (
    <tr id={`target-${t.name}`}>
      <td className="t-main"><b>{t.name}</b><div className="muted small">{t.type} · {t.envs}</div></td>
      <td data-label="Where" className="mono">{t.endpoint}<span className="sub">{t.bucket}/{t.prefix}</span></td>
      <td data-label="Keys">{t.badge ? <Pill tone={tone(t.badge.kind)}>{t.badge.text}</Pill> : null}{t.detail ? <div className="muted small">{t.detail}</div> : null}{t.at ? <div className="muted small">{t.at}</div> : null}</td>
      <td className="num" data-label="Retention">{t.retention} d<details className="fold bt-ilm"><summary>ILM</summary><pre className="code" role="region" aria-label={`ILM rule for ${t.name}`} tabIndex={0}><code>{t.ilm}</code></pre></details></td>
      <td className="num" data-label="Plans">{t.plans}</td>
      <td><div className="u-acts">
        <form action={settingsGo}><Hidden csrf={csrf} _to="backup-targets" action="test" name={t.name} /><Button variant="outline" size="sm" aria-label={`Test ${t.name}`}>Test target</Button></form>
        {t.plans ? <span className="muted small">Used by {plural(t.plans, "plan")}</span>
          : <details className="fold"><summary aria-label={`Remove ${t.name}`}>Remove…</summary><form action={settingsGo} className="u-confirm"><Hidden csrf={csrf} _to="backup-targets" action="remove" name={t.name} />
            <Button variant="destructive" size="sm">Remove {t.name}</Button><span className="muted small">Deletes the target {t.name} and its keys on {host}. New backups can no longer pick it; objects already in the bucket stay.</span></form></details>}
      </div></td>
    </tr>
  );
}

/** Settings → Backup targets: where backups go, each target's last test, and the add form. Keys go in once and never come back. */
export default async function BackupTargetsPage({ searchParams }: { searchParams: Search }) {
  const q = await pick(searchParams, ["req"]);
  const [r, csrf, flash] = await Promise.all([apiGet<AdminTargets>("/api/v1/admin/backup-targets" + qs(q)), csrfToken(), readFlash()]);
  if (r.status === 403) return <><header className="phead"><div><h1>Backup targets</h1></div></header><AdminsOnly /></>;
  if (r.status !== 200) throw new Error(`/api/v1/admin/backup-targets answered ${r.status}`);
  const d = r.data;
  return (
    <>
      <Refresh on={!!d.refresh} />
      <header className="phead"><div><h1>Backup targets</h1><p className="psub">{plural(d.rows.length, "target")}, added once and used by every New backup. Encryption keys (age) on {d.host}: <b>{d.age_recipients}</b></p></div></header>
      <SettingsTabs cur={PAGE} />
      <div className="stack">
        <Banners list={d.notices} flash={flash} />
        {d.age_recipients ? null : <Banner b={{ kind: "warn", text: `No age public key on ${d.host} yet (/etc/argus/backup-age-recipients.txt): Install now and the install bundle refuse until the operator adds the backup recipients.` }} />}
        <PlainCard as="section" className="l-card" aria-label="Targets">
          {d.rows.length ? <><div className="tablewrap" role="region" aria-label="Targets table" tabIndex={0}><table className="l-table">
            <thead><tr><th scope="col">Target</th><th scope="col">Where</th><th scope="col">Keys</th><th scope="col" className="num">Retention</th><th scope="col" className="num">Plans</th><th scope="col"><span className="sr">Actions</span></th></tr></thead>
            <tbody>{d.rows.map((t) => <Row key={t.name} t={t} csrf={csrf} host={d.host} />)}</tbody>
          </table></div></> : <p className="empty">No target yet. Add the first one below; every New backup then picks it.</p>}
        </PlainCard>
        <PlainCard as="section" id="add" aria-labelledby="add-h"><div className="card-h"><h2 id="add-h">Add a target</h2></div>
          <SetForm to="backup-targets" page={`${PAGE}#add`} csrf={csrf} form="target-add" stack hidden={{ action: "add" }} submit="Add and test"
            note={<>The keys go once to root on {d.host} (0600) and are never shown again, not even after a rejected save. The test lists the prefix, writes and deletes a test object under <span className="mono">prefix/.argus-test/</span>.</>}
            fields={[
              { name: "name", label: "Name", required: true, maxLength: 31, pattern: "[a-z0-9][a-z0-9\\-]{1,30}", placeholder: "develop-minio" },
              { name: "type", label: "Type", type: "select", value: "minio", options: [{ value: "minio", label: "MinIO" }, { value: "s3", label: "S3" }] },
              { name: "endpoint", label: "Endpoint", required: true, maxLength: 200, pattern: "https?://[A-Za-z0-9.\\-]+(:[0-9]{1,5})?/?", placeholder: "https://minio-backup.example:9000" },
              { name: "bucket", label: "Bucket", required: true, maxLength: 63, pattern: "[a-z0-9][a-z0-9.\\-]{1,61}[a-z0-9]", placeholder: "backup-develop" },
              { name: "prefix", label: "Prefix", maxLength: 120, placeholder: "argus", hint: "Jobs go under prefix/job." },
              { name: "access_key", label: "Access key", type: "password", required: true, minLength: 3, maxLength: 128 },
              { name: "secret_key", label: "Secret key", type: "password", required: true, minLength: 8, maxLength: 128 },
              { name: "provision", label: "These keys are a MinIO admin: create a write-only user per job (recommended)", type: "check", value: "yes", hint: "Without it every job uses these keys: give them a write-only policy (no delete), or a compromised host can wipe its backups." },
              { name: "retention_days", label: "Default retention, days", type: "number", value: "14", min: 1, max: 400 },
              { name: "envs", label: "For", type: "checks", hint: "None means all environments.", options: d.envs.map((e) => ({ value: e, label: e })) },
              { name: "ca_pem", label: "CA certificate", type: "area", rows: 3, maxLength: 20000, hint: "Optional, PEM, for an internal CA." },
            ]} /></PlainCard>
      </div>
    </>
  );
}

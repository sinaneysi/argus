import { HelpQ } from "@/components/HelpQ";
import type { Metadata } from "next";
import { AdminsOnly } from "@/components/Agent";
import { GeneralForm } from "@/components/GeneralForm";
import { Pill } from "@/components/Pills";
import { Banners, Hidden, When } from "@/components/Section";
import { SettingsTabs } from "@/components/Settings";
import { apiGet, csrfToken, readFlash } from "@/lib/server";
import type { AdminGeneral } from "@/lib/types";
import { setClaude } from "./actions";
import { Button } from "@/components/ui/button";
import { PlainCard } from "@/components/ui/card";

export const metadata: Metadata = { title: "General settings" };

function Creds({ rows }: { rows: AdminGeneral["credentials"]["ok"] }) {
  return <div className="tablewrap" role="region" aria-label="Credentials" tabIndex={0}><table className="l-table"><thead><tr><th>Status</th><th>Credential</th><th>Detail</th></tr></thead>
    <tbody>{rows.map((c) => <tr key={c.name}><td data-label="Status"><Pill tone={c.ok ? "ok" : "fail"}>{c.ok ? "OK" : "FAIL"}</Pill></td><td data-label="Credential">{c.name}</td><td className="muted" data-label="Detail">{c.detail}</td></tr>)}</tbody></table></div>;
}

export default async function AdminPage() {
  const [r, csrf, flash] = await Promise.all([apiGet<AdminGeneral>("/api/v1/admin"), csrfToken(), readFlash()]);
  const head = <header className="phead"><div><h1>General</h1><HelpQ p="/admin" /><p className="psub">Claude usage, schedules, thresholds, automatic RCA, readiness, QA questions, sign-in and credential status</p></div></header>;
  if (r.status === 403) return <>{head}<AdminsOnly /></>;
  if (r.status !== 200) throw new Error(`/api/v1/admin answered ${r.status}`);
  const d = r.data;
  const on = d.claude.on;
  return (
    <>
      {head}
      <SettingsTabs cur="/admin" />
      <div className="stack">
        <Banners list={d.applied.kind === "fail" ? [{ text: d.applied.text, kind: "fail" }] : []} flash={flash} />
        {d.applied.kind === "fail" ? null : <p className="muted small">{d.applied.text}</p>}
        <PlainCard as="section" id="claude-usage" aria-labelledby="g-c"><div className="card-h"><h2 id="g-c">Claude usage</h2><Pill tone={on ? "ok" : "warn"}>{on ? "On" : "Off"}</Pill></div>
          <p className="set-note">{on ? "Argus's model runs (triage, RCA, analyses, ideas, task runs, the fixer) use your Claude subscription." : "Off: Argus makes no Claude calls. Runs are skipped and recorded as skipped, never as failures; checks still collect their facts."} Applied by root within seconds, logged and posted to Matrix; on {d.host} also <span className="mono">argus-apply-settings claude on|off</span>.</p>
          <form action={setClaude}><Hidden csrf={csrf} claude={on ? "off" : "on"} /><Button variant={on ? "outline" : "default"}>{on ? "Turn off" : "Turn on"}</Button></form></PlainCard>
        <GeneralForm d={d} csrf={csrf} />
        <PlainCard as="section" aria-labelledby="g-cr"><div className="card-h"><h2 id="g-cr">Credentials</h2></div>
          <p className="set-note">Status only, values are never shown. {d.credentials.checkedAt ? <>Checked <When ts={d.credentials.checkedAt} />, hourly.</> : "No check has run yet."}</p>
          {d.credentials.bad.length ? <Creds rows={d.credentials.bad} /> : null}
          {d.credentials.ok.length ? <details className="fold"><summary>Credentials OK <span className="muted">{d.credentials.ok.length}</span></summary><Creds rows={d.credentials.ok} /></details> : null}</PlainCard>
        <PlainCard as="section" aria-labelledby="g-l"><div className="card-h"><h2 id="g-l">Change log</h2></div>
          {d.log.length ? <details className="fold"><summary>Last {d.log.length} changes</summary><ul className="log">{d.log.map((l, i) => <li key={i}>{l}</li>)}</ul></details> : <p className="set-note">No changes yet.</p>}</PlainCard>
      </div>
    </>
  );
}

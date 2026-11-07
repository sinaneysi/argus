import type { Metadata } from "next";
import { AdminsOnly } from "@/components/Agent";
import { EnvPill, Pill } from "@/components/Pills";
import { Banners, Hidden, pick, Refresh, tone, type Search } from "@/components/Section";
import { SetForm } from "@/components/SetForm";
import { SettingsTabs } from "@/components/Settings";
import { StatusBlock } from "@/components/StatusBlock";
import { qs } from "@/lib/api";
import { apiGet, csrfToken, readFlash } from "@/lib/server";
import type { AdminKube } from "@/lib/types";
import { settingsGo } from "../settings";
import { Button, buttonVariants } from "@/components/ui/button";
import { PlainCard } from "@/components/ui/card";
import { ConnectFold } from "@/components/Connect";

export const metadata: Metadata = { title: "Kubernetes settings" };

const PAGE = "/admin/kube";
const sub = <p className="psub">Read-only cluster access for the agent: get, describe, logs, events. Never Secrets, never a write. Every cluster is also listed in <a href="/admin/sources#src-t-kubernetes">Data sources</a>.</p>;

function Cluster({ c, csrf, host }: { c: AdminKube["clusters"][number]; csrf: string; host: string }) {
  return (
    <PlainCard as="section" id={`kube-c-${c.name}`} aria-labelledby={`kube-c-${c.name}-h`}>
      <div className="card-h"><h2 id={`kube-c-${c.name}-h`} className="mono">{c.name}</h2>{c.badge ? <Pill tone={tone(c.badge.kind)}>{c.badge.text}</Pill> : null}</div>
      <p className="set-note"><EnvPill env={c.env} /> {c.line}</p>
      <ConnectFold steps={c.connect} via={c.connectVia} />
      <p className="set-note">{c.agent}</p>
      {c.test ? <p className="set-note">{c.test}</p> : null}
      {c.why ? <p className="set-note">{c.why}</p> : null}
      {c.findings.length ? <><p className="set-note">What the check found:</p><ul className="set-list" aria-label="What the check found">{c.findings.map((x) => <li key={x}>{x}</li>)}</ul></> : null}
      <div className="row-act">
        <form action={settingsGo}><Hidden csrf={csrf} _to="kube" action="test" name={c.name} /><Button variant="outline">Check again</Button></form>
        <a data-slot="button" className={buttonVariants({ variant: "outline" })} href={`${PAGE}?replace=${encodeURIComponent(c.name)}#kube-add`}>Replace…</a>
      </div>
      <details className="fold"><summary>Remove…</summary><form action={settingsGo} className="u-confirm"><Hidden csrf={csrf} _to="kube" action="remove" name={c.name} />
        <Button variant="destructive" size="sm">Remove {c.name}</Button><span className="muted small">The agent loses access to {c.name} at once. Root keeps a backup of the kubeconfig in the backup folder on {host}.</span></form></details>
    </PlainCard>
  );
}

/** Settings → Kubernetes: the status, each cluster and the add or replace form. A kubeconfig goes in once and never comes back. */
export default async function KubePage({ searchParams }: { searchParams: Search }) {
  const q = await pick(searchParams, ["kr", "replace"]);
  const [r, csrf, flash] = await Promise.all([apiGet<AdminKube>("/api/v1/admin/kube" + qs(q)), csrfToken(), readFlash()]);
  if (r.status === 403) return <><header className="phead"><div><h1>Kubernetes</h1>{sub}</div></header><AdminsOnly /></>;
  if (r.status !== 200) throw new Error(`/api/v1/admin/kube answered ${r.status}`);
  const d = r.data;
  const rep = d.add.replace;
  return (
    <>
      <Refresh on={!!d.refresh} />
      <header className="phead"><div><h1>Kubernetes</h1>{sub}</div></header>
      <SettingsTabs cur={PAGE} />
      <div className="stack">
        <Banners list={d.notices} flash={flash} />
        <StatusBlock id="kube-now" s={d.status} />
        {d.clusters.map((c) => <Cluster key={c.name} c={c} csrf={csrf} host={d.host} />)}
        <PlainCard as="section" id="kube-add" aria-labelledby="kube-add-h"><div className="card-h"><h2 id="kube-add-h">Add or replace a cluster</h2></div>
          <p className="set-note">Saving a name that exists replaces its kubeconfig; root keeps a backup of the previous one.</p>
          <SetForm foldTitle={rep ? `Replace ${rep}` : "Add a cluster"} foldOpen={d.add.open} to="kube" page={`${PAGE}#kube-add`} csrf={csrf} form="kube" hidden={{ action: "save" }} submit="Save and check" stack note="Root stores and checks it within seconds."
            lead={<ol className="set-steps"><li>Create a read-only ServiceAccount and its kubeconfig (<a href="#kube-help">how</a>).</li><li>Paste the kubeconfig or choose its file, then <b>Save and check</b>. Root asks the cluster what this login may do. Only a read-only login is enabled for the agent.</li></ol>}
            fields={[
              { name: "name", label: "Cluster name", value: rep, maxLength: 31, required: true, pattern: "[a-z0-9][a-z0-9\\-]{0,30}", hint: <>Lowercase letters, digits and -, for example <span className="mono">dev</span>. The agent calls it by this name.</> },
              { name: "env", label: "Environment", type: "radio", value: d.add.env, options: [{ value: "develop", label: "Develop", sub: "Stage and test clusters." }, { value: "pre-release", label: "Pre-release", sub: "Pre-production." }, { value: "release", label: "Release", sub: "Production. Must be read-only: no override." }] },
              { name: "kubeconfig", label: "Kubeconfig", type: "area", rows: 8, hint: `YAML or JSON, up to 64 KB, with the token or client certificate inline. Exec and auth-provider plugins and file paths are refused, so nothing runs on ${d.host}. Sent once, to root only; never shown again or logged.` },
              { name: "kubeconfig_file", label: "Or choose the file", type: "file", accept: ".yaml,.yml,.json,.conf,.kubeconfig,text/plain" },
              { name: "context", label: "Context (optional)", maxLength: 121, hint: "Empty: the kubeconfig's current context. Root keeps only this context, its cluster and its user." },
              { name: "override", label: "Enable it even if it can write or read Secrets", type: "check", fold: "Advanced", hint: "Develop and pre-release only; release is always refused. Leave this off: the agent is meant to have a read-only login." },
            ]} /></PlainCard>
        <PlainCard as="section" id="kube-help" aria-labelledby="kube-help-h"><div className="card-h"><h2 id="kube-help-h">Make a read-only login</h2></div>
          <p className="set-note">Give Argus its own ServiceAccount that can get, list and watch, and nothing else. No Secrets: the role lists resources by name instead of <span className="mono">*</span>.</p>
          <details className="fold"><summary>ServiceAccount, role and kubeconfig</summary>
            <ol className="set-steps"><li>On a machine with admin access to the cluster, save this as <span className="mono">argus-ro.yaml</span>.</li></ol>
            <pre className="code" role="region" aria-label="argus-ro.yaml" tabIndex={0}><code>{d.help.yaml}</code></pre>
            <ol className="set-steps" start={2}><li>Apply it and build a kubeconfig with the token inline (replace <span className="mono">&lt;api-server&gt;</span>).</li></ol>
            <pre className="code" role="region" aria-label="Commands" tabIndex={0}><code>{d.help.cmds}</code></pre>
            <ol className="set-steps" start={3}><li>Add <span className="mono">argus-ro.kubeconfig</span> above. The check should say <b>Read-only</b>. Then delete the local copy.</li></ol></details></PlainCard>
      </div>
    </>
  );
}

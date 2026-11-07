import type { Metadata } from "next";
import { AdminsOnly } from "@/components/Agent";
import { EnvPill, Pill } from "@/components/Pills";
import { Banners, Hidden, pick, Refresh, tone, type Search } from "@/components/Section";
import { SetForm, type SetField } from "@/components/SetForm";
import { SettingsTabs } from "@/components/Settings";
import { StatusBlock } from "@/components/StatusBlock";
import { qs } from "@/lib/api";
import { apiGet, csrfToken, readFlash } from "@/lib/server";
import type { AdminSources } from "@/lib/types";
import { settingsGo } from "../settings";
import { Button, buttonVariants } from "@/components/ui/button";
import { PlainCard } from "@/components/ui/card";

export const metadata: Metadata = { title: "Data sources" };

const PAGE = "/admin/sources";
const sub = <p className="psub">Every system Argus reads, as many of each type as you have. Each login is checked: it must work, and the agent&apos;s must be read-only.</p>;
type Row = AdminSources["panels"][number]["rows"][number];
type Form = NonNullable<AdminSources["form"]>;

function Source({ r, csrf, host }: { r: Row; csrf: string; host: string }) {
  const ids = { type: r.type, name: r.name };
  return (
    <li className="src-row" id={`src-${r.type}-${r.name}`}>
      <div className="src-main">
        <div className="src-t">{r.badge ? <Pill tone={tone(r.badge.kind)}>{r.badge.text}</Pill> : null}<b className="mono">{r.name}</b>{r.env ? <EnvPill env={r.env} /> : null}{r.purpose ? <span className="pill neutral" title={r.purpose.title}>{r.purpose.label}</span> : null}</div>
        <p className="muted small">{r.meta.join(" · ")}</p>
        {r.detail ? <p className="set-note">{r.detail}</p> : null}
        {r.findings.length ? <ul className="set-list" aria-label="What the check found">{r.findings.map((x) => <li key={x}>{x}</li>)}</ul> : null}
        {r.where ? <p className="muted small">{r.where}</p> : null}
      </div>
      <div className="u-acts">
        {r.canCheck ? <form action={settingsGo}><Hidden csrf={csrf} _to="sources" action="test" {...ids} /><Button variant="outline" size="sm" aria-label={`Check ${r.name}`}>Check</Button></form> : null}
        {r.change ? <a data-slot="button" className={buttonVariants({ variant: "outline", size: "sm" })} href={r.change} aria-label={`Change ${r.name}`}>Change…</a> : null}
        {r.operator ? <span className="muted small">Set by the operator</span> : null}
        {r.editable ? <><a data-slot="button" className={buttonVariants({ variant: "outline", size: "sm" })} href={`${PAGE}?edit=${encodeURIComponent(r.key)}#src-add`} aria-label={`Edit ${r.name}`}>Edit…</a>
          <details className="fold"><summary aria-label={`Remove ${r.name}`}>Remove…</summary><form action={settingsGo} className="u-confirm"><Hidden csrf={csrf} _to="sources" action="remove" {...ids} />
            <Button variant="destructive" size="sm">Remove {r.name}</Button><span className="muted small">Argus stops reading {r.key} at once. Root keeps a backup of the credential in the backup folder on {host}.</span></form></details></> : null}
      </div>
    </li>
  );
}

function fields(f: Form, host: string): SetField[] {
  const edit = f.mode === "edit";
  const out: SetField[] = [];
  if (!edit) out.push({ name: "name", label: "Name", value: f.name, maxLength: 31, required: true, pattern: "[a-z0-9][a-z0-9\\-]{0,30}", hint: <>Lowercase letters, digits and -, for example <span className="mono">prod</span>. Unique per type; the agent calls it by this name.</> });
  out.push({ name: "env", label: "Environment", type: "radio", value: f.env, options: [{ value: "develop", label: "Develop", sub: "Stage and test." }, { value: "pre-release", label: "Pre-release", sub: "Pre-production." },
    { value: "release", label: "Release", sub: "Production. Read-only, no override." }, { value: "shared", label: "Shared", sub: "One system for every environment, such as GitLab. No override." }] });
  if (f.purposes.length > 1) out.push({ name: "purpose", label: "Used for", type: "radio", value: f.purpose, options: f.purposes });
  out.push({ name: "url", label: "Address", type: "url", value: f.url, maxLength: 300, required: true, placeholder: "https://", hint: `https only. Root calls it from ${host}.` });
  if (f.username) out.push({ name: "username", label: "Username (optional)", value: f.user, maxLength: 64, hint: "Empty: the field below is an API key." });
  out.push({ name: "credential", label: f.credLabel, type: "password", maxLength: 8192, required: !edit,
    hint: edit ? `Leave empty to keep the stored one${f.last4 ? ` (ends …${f.last4})` : ""}. ${f.credHint}` : `${f.credHint} Sent once, to root only; never shown again or logged.` });
  if (f.override) out.push({ name: "override", label: "Let the agent use it even if it can write", type: "check", on: f.overrideOn, fold: "Advanced", hint: "Develop and pre-release only; release and shared are always refused. Leave this off: the agent is meant to have a read-only login." });
  return out;
}

function AddForm({ f, csrf, host }: { f: Form; csrf: string; host: string }) {
  const edit = f.mode === "edit";
  const hidden: Record<string, string> = { action: "save", mode: f.mode, type: f.type };
  if (edit) hidden.name = f.name;
  if (f.purposes.length === 1) hidden.purpose = f.purposes[0]!.value;
  const here = edit ? `${PAGE}?edit=${encodeURIComponent(`${f.type}/${f.name}`)}#src-add` : `${PAGE}?add=${f.type}#src-add`;
  return (
    <PlainCard as="section" id="src-add" aria-labelledby="src-add-h">
      <div className="card-h"><h2 id="src-add-h">{edit ? `Edit ${f.label} ${f.name}` : `Add ${f.label}`}</h2><a className="muted small" href={edit ? PAGE : `${PAGE}?add=1#src-add`}>{edit ? "Cancel" : "Choose another type"}</a></div>
      <p className="set-note">{f.blurb} Root stores the credential and checks the login before the agent may use it.</p>
      <SetForm to="sources" page={here} csrf={csrf} form="src" hidden={hidden} fields={fields(f, host)} submit="Save and check" stack note="Root stores and checks it within seconds." />
    </PlainCard>
  );
}

/** Settings → Data sources: what Argus can read, and whether every login the agent uses is read-only. */
export default async function SourcesPage({ searchParams }: { searchParams: Search }) {
  const q = await pick(searchParams, ["sr", "add", "edit"]);
  const [r, csrf, flash] = await Promise.all([apiGet<AdminSources>("/api/v1/admin/sources" + qs(q)), csrfToken(), readFlash()]);
  if (r.status === 403) return <><header className="phead"><div><h1>Data sources</h1>{sub}</div></header><AdminsOnly /></>;
  if (r.status !== 200) throw new Error(`/api/v1/admin/sources answered ${r.status}`);
  const d = r.data;
  return (
    <>
      <Refresh on={!!d.refresh} />
      <header className="phead"><div><h1>Data sources</h1>{sub}</div></header>
      <SettingsTabs cur={PAGE} />
      <div className="stack">
        <Banners list={d.notices} flash={flash} />
        <StatusBlock id="src-now" s={d.status} />
        <p className="set-note"><a href="/admin/sources/catalogue">Catalogue</a>: where each service lives in each source, and the route decisions that wait for a person.</p>
        {d.block === "" && !d.busy ? <div className="row-act"><a data-slot="button" className={buttonVariants()} href={`${PAGE}?add=1#src-add`}>Add data source</a>
          {d.count ? <form action={settingsGo}><Hidden csrf={csrf} _to="sources" action="check-all" /><Button variant="outline">Check all now</Button></form> : null}</div> : null}
        {d.block === "picker" ? (
          <PlainCard as="section" id="src-add" aria-labelledby="src-add-h"><div className="card-h"><h2 id="src-add-h">Add a data source</h2><a className="muted small" href={PAGE}>Cancel</a></div>
            <ul className="src-list">{d.types?.map((t) => <li key={t.href} className="src-row"><div className="src-main"><a href={t.href}><b>{t.label}</b></a><p className="set-note">{t.blurb}</p></div></li>)}</ul>
            <p className="set-note">Coming next: {d.coming?.join(", ")}.</p></PlainCard>
        ) : null}
        {d.form ? <AddForm f={d.form} csrf={csrf} host={d.host} /> : null}
        {d.panels.map((p) => (
          <PlainCard as="section" key={p.key} id={`src-t-${p.key}`} aria-labelledby={`src-t-${p.key}-h`}>
            <div className="card-h"><h2 id={`src-t-${p.key}-h`}>{p.label} <span className="ncount">{p.rows.length}</span></h2>
              {p.managed ? <a className="muted small" href={p.managed}>Manage in Settings → {p.label}</a> : <a className="muted small" href={`${PAGE}?add=${p.key}#src-add`}>Add {p.label}</a>}</div>
            <ul className="src-list">{p.rows.map((x) => <Source key={x.key} r={x} csrf={csrf} host={d.host} />)}</ul>
          </PlainCard>
        ))}
      </div>
    </>
  );
}

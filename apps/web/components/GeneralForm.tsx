"use client";

import { useActionState, useEffect, useState } from "react";
import { Banner } from "./Banner";
import { Button, buttonVariants } from "./ui/button";
import { saveGeneral, type GeneralState } from "@/app/(app)/admin/actions";
import type { AdminGeneral, SettingField } from "@/lib/types";
import { PlainCard } from "@/components/ui/card";

const HOME = "/admin";
const id = (n: string) => "f-" + n.replace(":", "-");

function Field({ f, st }: { f: SettingField; st: GeneralState | null }) {
  const bad = st?.field === f.name;
  const v = st ? st.values[f.name] ?? "" : f.value ?? "";
  const a = { id: id(f.name), name: f.name, "aria-invalid": bad || undefined, "aria-describedby": bad ? id(f.name) + "-err" : undefined, autoFocus: bad || undefined };
  return (
    <div className="set-row">
      <label htmlFor={id(f.name)}>{f.label}</label>
      {f.type === "check" ? <input type="checkbox" value="1" defaultChecked={st ? st.values[f.name] === "1" : f.on} {...a} />
        : f.type === "number" ? <input type="number" min={f.lo} max={f.hi} defaultValue={v} {...a} />
          : f.type === "date" ? <input type="date" defaultValue={v} {...a} />
            : <input type="text" maxLength={40} defaultValue={v} {...a} />}
      <span className="set-range">{f.type === "check" ? "on/off" : f.type === "number" ? `${f.lo}–${f.hi}` : f.type}</span>
      {bad ? <span className="set-err" id={id(f.name) + "-err"}>{st?.error}</span> : null}
    </div>
  );
}

const Fold = ({ title, note, open, children }: { title: string; note: string; open?: boolean; children: React.ReactNode }) => (
  <PlainCard as="details" className="fold-t" open={open}><summary>{title} {note ? <span className="muted">{note}</span> : null}</summary>{children}</PlainCard>
);

/** The one form of General: a refused save re-renders with every entry kept and the field marked. Without JavaScript it still posts. */
export function GeneralForm({ d, csrf }: { d: AdminGeneral; csrf: string }) {
  const [st, action, busy] = useActionState<GeneralState | null, FormData>(saveGeneral, null, "/admin");
  const f = st?.field ?? "";
  // null until hydrated, so the bar stays usable without JavaScript; after that it wakes up on the first edit
  const [dirty, setDirty] = useState<boolean | null>(null);
  useEffect(() => setDirty((d) => d ?? false), []);
  const clean = dirty === false && !st;
  return (
    <form action={action} onChange={() => setDirty(true)} className="set-form" key={st ? JSON.stringify(st.values) : "stored"}>
      <input type="hidden" name="csrf" value={csrf} />
      {st ? <Banner b={{ text: st.error, kind: st.kind }} /> : null}
      <div className="set-grid">
        <PlainCard as="section" aria-labelledby="g-s"><div className="card-h"><h2 id="g-s">Schedules</h2></div>
          <div className="set-rows">{d.schedules.map((s) => {
            const n = `sched:${s.task}`;
            const bad = f === n;
            return (
              <div key={s.task} className="set-row job">
                <span>{s.label}</span>
                <select name={n} aria-label={`${s.label} schedule`} aria-invalid={bad || undefined} aria-describedby={bad ? `${id(n)}-err` : undefined} defaultValue={st ? st.values[n] : s.schedule}>{d.options.map((o) => <option key={o}>{o}</option>)}</select>
                <label className="check"><input type="checkbox" name={`on:${s.task}`} value="1" aria-label={`${s.label} enabled`} defaultChecked={st ? st.values[`on:${s.task}`] === "1" : s.enabled} />On</label>
                <span className="set-range" title={s.nextFull}>{s.next}</span>
                {bad ? <span className="set-err" id={`${id(n)}-err`}>{st?.error}</span> : null}
              </div>
            );
          })}</div></PlainCard>
        <PlainCard as="section" aria-labelledby="g-t"><div className="card-h"><h2 id="g-t">Thresholds</h2></div>
          <div className="set-rows">{d.thresholds.map((x) => <Field key={x.name} f={x} st={st} />)}</div></PlainCard>
      </div>
      <Fold title="Automatic deep RCA" note={d.notes.ra} open={f.startsWith("ra:")}>
        <p className="set-note">Root queues at most one deep RCA every 5 minutes for a qualifying incident, through the same limits as the button (1 per incident per hour, one at a time). Per incident at most one automatic run per 24 h, again only if its level goes up; one daily slot stays for the button.</p>
        <div className="set-rows">{d.rcaAuto.map((x) => <Field key={x.name} f={x} st={st} />)}</div></Fold>
      <Fold title="Hub services and sales-event readiness" note={d.notes.rd} open={f.startsWith("rd:")}>
        <p className="set-note">Tier-1 hub services (config/service-tiers.json) raise an incident&apos;s severity one step. The readiness section of the 09:00 brief and the Overview card run from D-N to D+N around the event date; the deploy-freeze reminder starts at D-N. Reports only.</p>
        <div className="set-rows">{d.readiness.map((x) => <Field key={x.name} f={x} st={st} />)}</div><input type="hidden" name="rd:present" value="1" /></Fold>
      <Fold title="QA follow-up questions" note={d.notes.qa}>
        <p className="set-note">When a {d.qaRoom} report is missing what Argus needs (the page, the environment, the time, the error text), Argus replies in the reporter&apos;s thread with 1 to 3 short questions in their language, prefixed &quot;Argus:&quot;. At most one question per issue, 10 a day, never about a suspicious message. Dry run shows the question on Conversations without sending it.</p>
        <div className="set-rows">{d.qa.map((x) => <Field key={x.name} f={x} st={st} />)}</div><input type="hidden" name="qa:present" value="1" /></Fold>
      <Fold title="Sign-in" note={d.sso.on ? "SSO only" : "password and SSO"}>
        <p className="set-note">{d.sso.note}</p>
        <div className="set-rows"><Field f={{ name: "auth:sso_only", type: "check", on: d.sso.on, label: `SSO only: password sign-in refused except for the break-glass account ${d.sso.breakGlass}` }} st={st} /></div></Fold>
      <div className="set-bar"><Button disabled={busy || clean}>{busy ? "Saving…" : "Save and apply"}</Button>{clean ? null : <a data-slot="button" className={buttonVariants({ variant: "outline" })} href={HOME}>Discard changes</a>}
        <span className="muted small">{clean ? "No changes. " : ""}Applied by root on {d.host} within seconds; every change is logged and posted to Matrix.</span></div>
    </form>
  );
}

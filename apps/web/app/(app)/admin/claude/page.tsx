import type { Metadata } from "next";
import { AdminsOnly } from "@/components/Agent";
import { Banners, Hidden, pick, Refresh, tone, type Search } from "@/components/Section";
import { Pill } from "@/components/Pills";
import { SetForm } from "@/components/SetForm";
import { SettingsTabs } from "@/components/Settings";
import { StatusBlock } from "@/components/StatusBlock";
import { qs } from "@/lib/api";
import { apiGet, csrfToken, readFlash } from "@/lib/server";
import type { AdminClaude } from "@/lib/types";
import { settingsGo } from "../settings";
import { Button } from "@/components/ui/button";
import { PlainCard } from "@/components/ui/card";

export const metadata: Metadata = { title: "Claude settings" };

const PAGE = "/admin/claude";
const sub = <p className="psub">Whether Argus&apos;s AI can run right now, and the login and checks behind it.</p>;
const Badge = ({ b }: { b: AdminClaude["token"]["badge"] }) => (b ? <Pill tone={tone(b.kind)}>{b.text}</Pill> : null);

/** Settings → Claude: one status block answers "is the AI working?", then token, location guard and usage. */
export default async function ClaudePage({ searchParams }: { searchParams: Search }) {
  const q = await pick(searchParams, ["ct"]);
  const [r, csrf, flash] = await Promise.all([apiGet<AdminClaude>("/api/v1/admin/claude" + qs(q)), csrfToken(), readFlash()]);
  if (r.status === 403) return <><header className="phead"><div><h1>Claude</h1>{sub}</div></header><AdminsOnly /></>;
  if (r.status !== 200) throw new Error(`/api/v1/admin/claude answered ${r.status}`);
  const d = r.data;
  const s = d.status;
  const on = d.usage.on;
  return (
    <>
      <Refresh on={!!d.refresh} />
      <header className="phead"><div><h1>Claude</h1>{sub}</div></header>
      <SettingsTabs cur={PAGE} />
      <div className="stack">
        <Banners list={d.notices} flash={flash} />
        <StatusBlock id="claude-now" s={s} />
        <PlainCard as="section" id="claude-token" aria-labelledby="cl-t"><div className="card-h"><h2 id="cl-t">Claude token</h2><Badge b={d.token.badge} /></div>
          <p className="set-note">{d.token.line}</p>
          {d.token.why ? <p className="set-note">{d.token.why}</p> : null}
          <Banners list={d.token.warn} />
          <form action={settingsGo} className="row-act"><Hidden csrf={csrf} _to="claude-token" /><Button name="action" value="test" variant="outline">Test current token</Button><span className="muted small">Asks Claude one short question. Changes nothing.</span></form>
          <SetForm foldTitle="Replace the token" foldId="claude-replace" foldOpen={d.token.open} to="claude-token" page={`${PAGE}#claude-token`} csrf={csrf} form="claude-token" hidden={{ action: "replace", kind: "oauth" }} who={d.user} submit="Replace and test" stack
            lead={<ol className="set-steps"><li>On a computer signed in to the Claude account Argus should use, run <code>claude setup-token</code>. Copy the token it prints. It starts with <code>sk-ant-oat01-</code>.</li><li>Paste it below and choose <b>Replace and test</b>. If the test fails, the previous token goes back.</li></ol>}
            fields={[{ name: "token", label: "New token", type: "password", maxLength: 400, required: true, hint: "Sent once, to root only. Never shown or logged; the audit log keeps who changed it and its last 4 characters." },
              { name: "save_anyway", label: "Keep the new token even if the test fails", type: "check", fold: "Advanced", hint: "Only when you are sure the token is right and Claude itself is down. Otherwise leave this off: the previous token comes back." }]} /></PlainCard>
        <PlainCard as="section" id="claude-egress" aria-labelledby="cl-e"><div className="card-h"><h2 id="cl-e">Location guard</h2><Badge b={d.egress.badge} /></div>
          <p className="set-note">{d.egress.line}</p>
          {d.egress.why ? <p className="set-note">{d.egress.why}</p> : null}
          <p className="set-note">Stops AI if Claude traffic would leave from a country Anthropic does not serve.</p>
          <form action={settingsGo} className="row-act"><Hidden csrf={csrf} _to="claude-check" /><Button name="action" value="egress" variant="outline">Check now</Button></form>
          <SetForm foldTitle={<>Guard settings <span className="muted small">{d.egress.summary}</span></>} to="claude-egress" page={`${PAGE}#claude-egress`} csrf={csrf} form="claude-egress" secondary submit="Save guard settings" stack note="Root applies it within seconds."
            lead={<p className="set-note">Before every run Argus checks where its Claude traffic leaves from. Calls from a country Anthropic does not serve can get the account disabled, so the guard stops AI and says why. It never changes the route.</p>}
            fields={[{ name: "guard", label: "Stop AI when the country is blocked", type: "check", on: d.egress.guard, hint: "On by default. Off: runs start wherever the traffic leaves from." },
              { name: "blocked", label: "Blocked countries", value: d.egress.blocked, maxLength: 200, placeholder: "IR", hint: "Two-letter country codes, separated by commas, for example IR, KP." },
              { name: "fail", label: "If the country lookup fails", type: "radio", value: d.egress.fail, options: [{ value: "closed", label: "Stop the run", sub: "The default, and safer for the account." }, { value: "open", label: "Let the run start", sub: "Only when the lookup services are known to be unreachable from here." }] }]} /></PlainCard>
        <PlainCard as="section" id="claude-usage" aria-labelledby="cl-u"><div className="card-h"><h2 id="cl-u">Claude usage</h2><Pill tone={on ? "ok" : "neutral"}>{on ? "On" : "Off"}</Pill></div>
          <p className="set-note">{on ? <><b>On.</b> Argus&apos;s checks, RCA, analyses and agents use the Claude subscription.</> : <><b>Off.</b> No model calls. Runs are recorded as skipped, not failed, and checks still collect their facts.</>}</p>
          <form action={settingsGo} className="row-act"><Hidden csrf={csrf} _to="claude-usage" claude={on ? "off" : "on"} /><Button variant={on ? "outline" : "default"}>{on ? "Turn off" : "Turn on"}</Button><span className="muted small">Root applies it within seconds and posts it to Matrix.</span></form></PlainCard>
      </div>
    </>
  );
}

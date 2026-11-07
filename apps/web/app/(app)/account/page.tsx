import type { Metadata } from "next";
import { redirect } from "next/navigation";
import { Banner } from "@/components/Banner";
import { Pill } from "@/components/Pills";
import { Banners, Hidden, pick, Refresh, tone, type Search, When } from "@/components/Section";
import { SetForm } from "@/components/SetForm";
import "@/components/settings.css";
import { qs } from "@/lib/api";
import { safeNext } from "@/lib/format";
import { apiGet, csrfToken, readFlash } from "@/lib/server";
import type { Account } from "@/lib/types";
import { settingsGo } from "../admin/settings";
import { Button, buttonVariants } from "@/components/ui/button";
import { PlainCard } from "@/components/ui/card";

export const metadata: Metadata = { title: "Account" };

const PAGE = "/account";
type Jira = NonNullable<Account["jira"]>;

function JiraConnect({ a, j, csrf, fold }: { a: Account; j: Jira; csrf: string; fold?: string }) {
  return (
    <SetForm to="account-jira" page={`${PAGE}#jira`} csrf={csrf} form="jira-connect" stack hidden={{ action: "connect" }} who={a.user} submit="Test and save" foldTitle={fold}
      fields={[
        { name: "url", label: "Jira URL", type: "url", value: j.url, placeholder: "https://jira.example.com", maxLength: 300, required: true, hint: j.allowed },
        { name: "token", label: "Personal access token", type: "password", maxLength: 512, required: true, hint: `Jira: Profile → Personal Access Tokens → Create token. It is sent once to ${a.host} and stored there for root only; Argus never shows it again.` },
        { name: "mode", label: "Mode", type: "radio", value: j.mode, options: [{ value: "ro", label: "Read-only", sub: "See your Jira issues on the board; changes stay in Jira." }, { value: "rw", label: "Read and write", sub: "Moves, comments, assignment and new tasks from Argus use your token." }] },
      ]} />
  );
}

function Jira({ a, csrf }: { a: Account; csrf: string }) {
  const j = a.jira!;
  return (
    <PlainCard as="section" id="jira" aria-labelledby="jira-h"><div className="card-h"><h2 id="jira-h">Jira</h2><Pill tone={j.connected ? "ok" : "neutral"}>{j.connected ? "connected" : "not connected"}</Pill></div>
      <Banners list={j.notices} />
      {!j.connected ? <><p className="set-note">Connect your own Jira account to see the issues you can see in Jira on the board. Jira stays the source of truth.</p><JiraConnect a={a} j={j} csrf={csrf} /></> : <>
        {j.chosen?.length ? null : <Banner b={{ kind: "warn", text: "Choose the projects to show; nothing is pulled until you do." }} />}
        <dl className="set-kv">
          <div><dt>Connected as</dt><dd>{j.display} <span className="muted small mono">{j.jira_name}</span></dd></div>
          <div><dt>Jira</dt><dd className="mono small">{j.jira_url}</dd></div>
          <div><dt>Version</dt><dd>{j.version}</dd></div>
          <div><dt>Mode</dt><dd>{j.mode === "rw" ? "Read and write" : "Read-only"}</dd></div>
          <div><dt>Token expiry</dt><dd className="muted">not reported by the Jira API Argus may call; see Profile → Personal Access Tokens in Jira</dd></div>
          <div><dt>Last sync</dt><dd>{j.last_sync ? <When ts={j.last_sync} /> : <span className="muted">not yet</span>}</dd></div>
          <div><dt>Last error</dt><dd>{j.last_error ? <Pill tone="fail">{j.last_error}</Pill> : <span className="muted">none</span>}</dd></div>
        </dl>
        <h3>Projects on the board</h3>
        <SetForm to="account-jira" page={`${PAGE}#jira`} csrf={csrf} form="jira-projects" stack secondary hidden={{ action: "projects" }} submit="Save projects"
          fields={[
            ...(j.projects?.length ? [{ name: "project", label: "Projects", type: "checks" as const, value: j.chosen?.join(","), options: j.projects.map((p) => ({ value: p.key, label: p.key, sub: p.name })) }] : []),
            { name: "default_project", label: "New tasks go to", type: "select" as const, value: j.default_project, options: [{ value: "", label: "first chosen" }, ...(j.chosen ?? []).map((k) => ({ value: k, label: k }))] },
            { name: "mode", label: "Mode", type: "select" as const, value: j.mode, options: [{ value: "ro", label: "Read-only" }, { value: "rw", label: "Read and write" }] },
          ]} note={j.projects?.length ? undefined : "Your token sees no project."} />
        <JiraConnect a={a} j={j} csrf={csrf} fold="Replace the token or change the URL" />
        <details className="fold"><summary aria-label="Disconnect Jira">Disconnect…</summary><form action={settingsGo} className="u-confirm"><Hidden csrf={csrf} _to="account-jira" action="disconnect" />
          <Button variant="destructive" size="sm">Disconnect</Button><span className="muted small">Deletes the stored token on {a.host}. Revoke it in Jira too.</span></form></details>
      </>}
    </PlainCard>
  );
}

function Sessions({ a, csrf }: { a: Account; csrf: string }) {
  const l = a.limits!;
  const list = a.sessions ?? [];
  return (
    <PlainCard as="section" className="l-card" aria-labelledby="sess-h"><div className="card-h pad"><h2 id="sess-h">Your sessions <span className="ncount">{list.length}</span></h2></div>
      <p className="set-note pad">Every browser where you are signed in. Don&apos;t recognise one? Sign it out and change your password. Kept signed in lasts up to {l.remember_days} days ({l.remember_idle} days unused); otherwise {l.session_hours} hours ({l.idle_hours} hours idle).</p>
      <div className="tablewrap" role="region" aria-label="Your sessions" tabIndex={0}><table className="l-table">
        <thead><tr><th scope="col">Device</th><th scope="col">IP</th><th scope="col">Last seen</th><th scope="col">Signed in</th><th scope="col"><span className="sr">Actions</span></th></tr></thead>
        <tbody>{list.map((s) => (
          <tr key={s.id}>
            <td className="t-main"><b>{s.device}</b>{s.current ? <> <Pill>this device</Pill></> : null}<div className="muted small">{s.kind}</div></td>
            <td className="mono" data-label="IP">{s.ip}</td>
            <td className="nw" data-label="Last seen"><When ts={s.last_seen} /></td>
            <td className="nw muted small" data-label="Signed in">{s.created}</td>
            <td><form action={settingsGo}><Hidden csrf={csrf} _to="account-sessions" action="revoke" id={s.id} /><Button variant="outline" size="sm" aria-label={s.current ? "Sign out this session" : `Sign out ${s.device}, last seen from ${s.ip}`}>{s.current ? "Sign out this session" : "Sign out"}</Button></form></td>
          </tr>
        ))}</tbody>
      </table></div>
      {list.length > 1 ? <form action={settingsGo} className="pad"><Hidden csrf={csrf} _to="account-sessions" action="others" /><Button variant="destructive" size="sm">Sign out everywhere else</Button></form> : null}
    </PlainCard>
  );
}

/** Account: the password (a local account), two-factor, sessions, and this person's Jira and Matrix. */
export default async function AccountPage({ searchParams }: { searchParams: Search }) {
  const q = await pick(searchParams, ["jira", "matrix"]);
  const [r, csrf, flash] = await Promise.all([apiGet<Account>("/api/v1/account" + qs(q)), csrfToken(), readFlash()]);
  if (r.status === 403) redirect(safeNext((r.data as { next?: string }).next, "/account/2fa"));
  if (r.status !== 200) throw new Error(`/api/v1/account answered ${r.status}`);
  const a = r.data;
  const m = a.mfa;
  return (
    <>
      <Refresh on={!!a.refresh} />
      <header className="phead"><div><h1>Account</h1><p className="psub"><span className="mono">{a.user}</span> <Pill>{a.role}</Pill>{a.sso ? <> <Pill>keycloak</Pill></> : null}</p></div></header>
      <div className="stack">
        {a.must_change ? <Banner b={{ kind: "fail", text: "Choose a new password before continuing." }} /> : null}
        <Banners flash={flash} />
        {a.sso ? <PlainCard as="section" aria-labelledby="sso-h"><div className="card-h"><h2 id="sso-h">Single sign-on</h2></div><p className="set-note">You sign in with Keycloak ({a.sso.issuer}). Your password is managed there, and your Argus role comes from the Keycloak client roles of <span className="mono">{a.sso.client}</span> at every sign-in.</p></PlainCard>
          : <PlainCard as="section" aria-labelledby="pw-h"><div className="card-h"><h2 id="pw-h">Change password</h2></div><p className="set-note">Use at least {a.min_pw} characters.</p>
            <SetForm to="account" page={PAGE} csrf={csrf} form="password" stack who={a.user} secondary={!a.must_change} submit="Change password"
              fields={[
                { name: "current", label: "Current password", type: "password", required: true, autoComplete: "current-password" },
                { name: "new", label: `New password (at least ${a.min_pw} characters)`, type: "password", required: true, autoComplete: "new-password" },
                { name: "new2", label: "New password again", type: "password", required: true, autoComplete: "new-password" },
              ]} /></PlainCard>}
        {m ? <PlainCard as="section" aria-labelledby="mfa-h"><div className="card-h"><h2 id="mfa-h">Two-factor authentication</h2><Pill tone={tone(m.kind)}>{m.state}</Pill></div><p className="set-note">{m.note}</p><p className="row-act"><a data-slot="button" className={buttonVariants({ variant: "outline" })} href="/account/2fa">{m.enabled ? "Manage two-factor" : "Set up two-factor"}</a></p></PlainCard> : null}
        {a.sessions ? <Sessions a={a} csrf={csrf} /> : null}
        {a.jira ? <Jira a={a} csrf={csrf} /> : null}
        {a.matrix ? <PlainCard as="section" id="matrix" aria-labelledby="mx-h"><div className="card-h"><h2 id="mx-h">Matrix</h2><Pill tone={a.matrix.id ? "ok" : "neutral"}>{a.matrix.id ? "linked" : "not linked"}</Pill></div>
          {a.matrix.saved ? <Banner b={{ kind: "ok", text: "Saved." }} /> : null}
          <p className="set-note">{a.matrix.note}</p>
          <SetForm to="account-matrix" page={`${PAGE}#matrix`} csrf={csrf} form="matrix" stack secondary submit="Save" fields={[{ name: "matrix_id", label: "Your Matrix ID", value: a.matrix.id, maxLength: 250, placeholder: a.matrix.placeholder }]} /></PlainCard> : null}
      </div>
    </>
  );
}

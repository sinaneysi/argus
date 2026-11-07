"use client";

import { useActionState, useEffect, useRef } from "react";
import { Banner } from "./Banner";
import { userAct, type UsersState } from "@/app/(app)/admin/actions";
import type { AdminUser, AdminUsers } from "@/lib/types";
import { Button } from "@/components/ui/button";
import { PlainCard } from "@/components/ui/card";
import { Icon } from "./Icon";

const USER = "[a-z0-9][a-z0-9._-]{1,39}";
const when = (t: number) => (t ? new Date(t * 1000).toISOString().slice(0, 16).replace("T", " ") : "-");

type Ctl = { action: (fd: FormData) => void; csrf: string; busy: boolean };

const Hid = ({ csrf, user }: { csrf: string; user?: string }) => <><input type="hidden" name="csrf" value={csrf} />{user ? <input type="hidden" name="user" value={user} /> : null}</>;

// The ids of one row's menu and confirm steps (usernames are [a-z0-9._-], safe in an id).
const pid = (u: AdminUser, a: string) => `u-${a}-${u.name.replace(/[^a-z0-9_-]/g, "_")}`;

/** A menu item that acts at once: a change that is easy to undo. */
function Now({ c, u, a, label }: { c: Ctl; u: AdminUser; a: string; label: string }) {
  return <form action={c.action}><Hid csrf={c.csrf} user={u.name} /><Button name="action" value={a} disabled={c.busy} variant="ghost" size="sm" className="u-item">{label}</Button></form>;
}

/** A menu item that opens the action's own confirm step (no JavaScript: the popover attribute). */
function Ask({ u, a, label, danger }: { u: AdminUser; a: string; label: string; danger?: boolean }) {
  return <Button type="button" popoverTarget={pid(u, a)} variant={danger ? "destructive-ghost" : "ghost"} size="sm" className="u-item">{label}…</Button>;
}

type Step = { a: string; title: (n: React.ReactNode) => React.ReactNode; facts: string[]; go: string; reason?: boolean };

/** One focused confirm step: what happens, in plain words, then Cancel or the action. Only one is open at a time. */
function Confirm({ c, u, s }: { c: Ctl; u: AdminUser; s: Step }) {
  const id = pid(u, s.a);
  return (
    <div popover="auto" id={id} className="u-pop u-ask" role="dialog" aria-labelledby={`${id}-h`}>
      <form action={c.action} className="u-ask-f"><Hid csrf={c.csrf} user={u.name} />
        <h2 id={`${id}-h`}>{s.title(<span className="mono">{u.name}</span>)}</h2>
        <ul className="u-facts">{s.facts.map((f) => <li key={f}>{f}</li>)}</ul>
        {s.reason ? <label className="field"><span>Why (at least 10 characters; kept in the security events)</span><input name="reason" minLength={10} maxLength={300} required placeholder="Lost phone, identity checked by call" autoComplete="off" /></label> : null}
        <div className="u-ask-b">
          <Button type="button" popoverTarget={id} popoverTargetAction="hide" variant="outline" size="sm">Cancel</Button>
          <Button name="action" value={s.a} disabled={c.busy} variant="destructive" size="sm">{c.busy ? "Working…" : s.go}</Button>
        </div>
      </form>
    </div>
  );
}

const STEPS: Record<string, Step> = {
  reset: { a: "reset", title: (n) => <>Reset the password of {n}?</>, go: "Reset password",
    facts: ["Their current password stops working at once.", "A temporary password is shown here once. Give it to them safely.", "They must choose a new password at their next sign-in."] },
  "role-admin": { a: "role-admin", title: (n) => <>Make {n} an admin?</>, go: "Make admin",
    facts: ["They get full admin rights: settings, approvals and user accounts.", "You can make them a viewer again at any time."] },
  disable: { a: "disable", title: (n) => <>Disable {n}?</>, go: "Disable account",
    facts: ["They cannot sign in, with a password or Keycloak, until an admin enables the account again.", "Sessions that are open now stay open: use Sign out everywhere to end them."] },
  "revoke-sessions": { a: "revoke-sessions", title: (n) => <>Sign {n} out everywhere?</>, go: "Sign out everywhere",
    facts: ["Every browser where they are signed in is signed out now.", "They can sign in again at once."] },
  "reset-2fa": { a: "reset-2fa", title: (n) => <>Reset two-factor authentication of {n}?</>, go: "Reset 2FA", reason: true,
    facts: ["Their authenticator app and recovery codes stop working, and every session ends.", "You do not set up their new app.",
      "At their next sign-in Argus opens only the two-factor setup until they confirm a new app."] },
};

/** The row's one entry point: Actions opens a short menu; a risky item opens its own confirm step. */
function Acts({ c, u }: { c: Ctl; u: AdminUser }) {
  const has = (a: string) => u.actions.includes(a);
  if (!u.actions.length) return null;
  const menu = pid(u, "menu");
  const risky = ["reset", "role-admin", "revoke-sessions"].filter(has);
  const worst = ["disable", "reset-2fa"].filter(has);
  return (
    <div className="u-acts">
      <Button type="button" popoverTarget={menu} variant="outline" size="sm" className="u-open" aria-label={`Actions for ${u.name}`}>Actions <Icon name="chevron" size={14} className="u-chev" /></Button>
      <div popover="auto" id={menu} className="u-pop u-menu" role="group" aria-label={`Actions for ${u.name}`}>
        {has("role-viewer") ? <Now c={c} u={u} a="role-viewer" label="Make viewer" /> : null}
        {has("enable") ? <Now c={c} u={u} a="enable" label="Enable account" /> : null}
        {risky.map((a) => <Ask key={a} u={u} a={a} label={STEPS[a]!.go} />)}
        {worst.length ? <hr className="u-sep" /> : null}
        {worst.map((a) => <Ask key={a} u={u} a={a} label={STEPS[a]!.go} danger />)}
      </div>
      {[...risky, ...worst].map((a) => <Confirm key={a} c={c} u={u} s={STEPS[a]!} />)}
    </div>
  );
}

/** The tables and forms of Users and access. Every form posts through one action, so its answer (which can be a temporary
 * password, shown once) stays out of cookies and the URL. Without JavaScript the forms still post. */
export function UsersBody({ d, csrf }: { d: AdminUsers; csrf: string }) {
  const [st, action, busy] = useActionState<UsersState | null, FormData>(userAct, null, "/admin/users");
  const c: Ctl = { action, csrf, busy };
  const answer = useRef<HTMLDivElement>(null);
  useEffect(() => { // close the menu and confirm step that posted; the answer may be a password shown once
    if (!st) return;
    document.querySelectorAll<HTMLElement>(".u-pop:popover-open").forEach((p) => p.hidePopover());
    answer.current?.scrollIntoView({ block: "center" });
  }, [st]);
  return (
    <>
      <div ref={answer}>{st ? <Banner b={{ text: st.text, kind: st.kind }} /> : null}</div>
      <PlainCard as="section" className="l-card" aria-label="Accounts"><div className="tablewrap" role="region" aria-label="Accounts" tabIndex={0}><table className="l-table u-table">
        <thead><tr><th>User</th><th>Role</th><th>Source</th><th>2FA</th><th>Last login</th><th>Matrix ID</th><th>State</th><th><span className="sr">Actions</span></th></tr></thead>
        <tbody>{d.rows.map((u) => (
          <tr key={u.name}><td className="mono">{u.name}{u.you ? <span className="muted small"> (you)</span> : null}</td><td data-label="Role"><span className="pill neutral">{u.role}</span></td><td data-label="Source"><span className="pill neutral">{u.source}</span></td>
            <td data-label="2FA">{u.mfa ? <><span className="pill ok">on</span><br /><span className="muted small">{u.mfaUsed ? <>used <span className="nw">{when(u.mfaUsed)}</span></> : "not used yet"}</span></> : <span className="pill neutral">off</span>}</td>
            <td className="muted small" data-label="Last login">{when(u.lastLogin)}</td><td className="mono small" data-label="Matrix ID">{u.matrix || <span className="muted">-</span>}</td>
            <td data-label="State"><span className={`pill ${u.stateKind}`}>{u.state}</span></td><td><Acts c={c} u={u} /></td></tr>
        ))}</tbody></table></div></PlainCard>
      <PlainCard as="section" aria-labelledby="u-m"><div className="card-h"><h2 id="u-m">Two-factor authentication</h2></div>
        <p className="set-note">{d.mfa.note}{d.mfa.required ? <><b>Required at sign-in:</b> users without 2FA must set it up before anything else.</> : "At sign-in it is optional: users without 2FA can read and are asked to set it up at their first change."}</p>
        {d.mfa.fromEnv ? <p className="muted small">Set by MFA_REQUIRED_FOR_LOGIN=1 on the server.</p> : <form action={action}><Hid csrf={csrf} /><Button name="action" value={d.mfa.required ? "mfa-login-off" : "mfa-login-on"} disabled={busy} variant="outline">{d.mfa.required ? "Stop requiring 2FA at sign-in" : "Require 2FA for all users at next login"}</Button></form>}
        {d.mfa.events.length ? <details className="fold"><summary>Recent security events <span className="muted">{d.mfa.events.length}</span></summary><div className="tablewrap" role="region" aria-label="Recent security events" tabIndex={0}><table className="l-table"><thead><tr><th>When</th><th>Who</th><th>What</th><th>User</th><th>Detail</th></tr></thead>
          <tbody>{d.mfa.events.map((e, i) => <tr key={i}><td className="muted small" data-label="When">{when(e.at)}</td><td className="mono" data-label="Who">{e.actor}</td><td data-label="What">{e.action}{e.outcome !== "ok" ? <> <span className="pill warn">{e.outcome}</span></> : null}</td><td className="mono" data-label="User">{e.target}</td><td className="small" data-label="Detail">{e.detail}{e.reason ? ` · ${e.reason}` : ""}</td></tr>)}</tbody></table></div></details> : null}</PlainCard>
      <PlainCard as="section" aria-labelledby="u-x"><div className="card-h"><h2 id="u-x">Matrix IDs</h2><span className="muted small">{d.matrix.linked} linked</span></div>
        <p className="set-note">Admins with a Matrix ID can direct-message tasks to Argus{d.matrix.bot ? ` (${d.matrix.bot})` : ""}; invites from anyone else are rejected.</p>
        <form action={action} className="u-add"><Hid csrf={csrf} />
          <label className="field"><span>User</span><select name="user">{d.names.map((n) => <option key={n}>{n}</option>)}</select></label>
          <label className="field"><span>Matrix ID</span><input name="matrix_id" maxLength={250} placeholder={`@name:${d.matrix.server} (empty clears)`} spellCheck={false} autoComplete="off" /></label>
          <Button disabled={busy} variant="outline">Save</Button></form></PlainCard>
      <PlainCard as="section" aria-labelledby="add-user"><div className="card-h"><h2 id="add-user">Add user</h2></div>
        <p className="set-note">New users and password resets get a temporary password, shown once here, that must be changed at first login. Keycloak users get their role from Keycloak at every sign-in and have no password here; disabling one here still locks them out.</p>
        <form action={action} className="u-add"><Hid csrf={csrf} /><input type="hidden" name="action" value="add" />
          <label className="field"><span>Username</span><input name="user" required pattern={USER} aria-describedby="u-hint" autoComplete="off" /></label>
          <label className="field"><span>Role</span><select name="role"><option>viewer</option><option>admin</option></select></label>
          <Button disabled={busy}>Add user</Button></form>
        <p className="muted small" id="u-hint">2–40 characters: a–z, 0–9, dot, dash, underscore; starts with a letter or digit</p></PlainCard>
    </>
  );
}

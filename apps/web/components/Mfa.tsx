"use client";

import { useActionState } from "react";
import { mfaAct, type Codes, type Enrol, type MfaState } from "@/app/(app)/account/2fa/actions";
import type { AccountMfa } from "@/lib/types";
import { Banner } from "./Banner";
import { Pill } from "./Pills";
import { When } from "./Section";
import { Button, buttonVariants } from "@/components/ui/button";
import { PlainCard } from "@/components/ui/card";

const PATTERN = "[0-9 ]{6,7}|[A-Za-z0-9 \\-]{16,19}";
type Act = (fd: FormData) => void;

function CodeField({ id }: { id: string }) {
  return (
    <div className="field"><label htmlFor={id}>6-digit code from your app</label>
      <input id={id} name="code" inputMode="numeric" autoComplete="one-time-code" pattern={PATTERN} maxLength={19} required spellCheck={false} autoCapitalize="off" /></div>
  );
}

const Hidden = ({ csrf, to, next }: { csrf: string; to: string; next: string }) => <><input type="hidden" name="csrf" value={csrf} /><input type="hidden" name="_to" value={to} /><input type="hidden" name="next" value={next} /></>;
const Err = ({ st }: { st: MfaState | null }) => (st?.error ? <Banner b={{ text: st.error, kind: st.kind ?? "fail" }} /> : null);

function Password({ id, user }: { id: string; user: string }) {
  return <><input type="text" className="sr" name="who" value={user} autoComplete="username" readOnly tabIndex={-1} aria-hidden="true" /><div className="field"><label htmlFor={id}>Your password</label><input id={id} name="password" type="password" autoComplete="current-password" required /></div></>;
}

/** Recovery codes, shown once: from the answer of the action that made them. */
function CodesSheet({ c, user }: { c: Codes; user: string }) {
  return (
    <PlainCard as="section" className="mfa-sheet" aria-labelledby="mfa-codes-h">
      <Banner b={{ text: c.note, kind: "ok" }} />
      <div className="card-h"><h2 id="mfa-codes-h">Save your recovery codes</h2></div>
      <p className="set-note">If you lose your phone, type one of these instead of a code from the app. Each works once. Argus does not show them again: copy them into your password manager, or print this page.</p>
      <p className="mfa-print">Argus recovery codes for <span className="mono">{user}</span>, made {c.made}. Each works once.</p>
      <ol className="mfa-codes">{c.codes.map((x) => <li key={x} className="mono">{x}</li>)}</ol>
      <div className="mfa-ack"><label className="set-chk"><input type="checkbox" /><span>I saved these codes somewhere safe</span></label><a data-slot="button" className={buttonVariants()} href={c.next}>Continue</a></div>
    </PlainCard>
  );
}

function EnrolStep({ e, st, act, busy, csrf, next }: { e: Enrol; st: MfaState | null; act: Act; busy: boolean; csrf: string; next: string }) {
  return (
    <PlainCard as="section" aria-label="Set up two-factor authentication"><ol className="mfa-steps">
      <li><h2>Add Argus to your authenticator app</h2>
        <p className="set-note">In the app, add an account and scan this code (Google Authenticator: <b>+</b>, then <b>Scan a QR code</b>).</p>
        <div className="mfa-qrwrap">
          <div className="mfa-qr"><svg viewBox={`0 0 ${e.qr.n} ${e.qr.n}`} role="img" aria-label="QR code that adds Argus to your authenticator app" shapeRendering="crispEdges"><rect width={e.qr.n} height={e.qr.n} fill="#ffffff" /><path fill="#000000" d={e.qr.d} /></svg></div>
          <div className="mfa-key"><p className="small">Cannot scan? Choose <b>Enter a setup key</b> in the app and type this key (time based):</p>
            <p className="mono mfa-seed">{e.key}</p><p className="small muted">Account name: <span className="mono">{e.account}</span></p>
            <details className="fold"><summary>Show the setup link</summary><p className="mono small mfa-uri">{e.uri}</p></details></div>
        </div></li>
      <li><h2>Type the code the app shows</h2>
        <Err st={st} />
        <form action={act} className="set-form set-stack" autoComplete="off"><Hidden csrf={csrf} to="confirm" next={next} /><CodeField id="mfa-confirm" /><p className="row-act"><Button  disabled={busy}>{busy ? "Checking…" : "Turn on two-factor authentication"}</Button></p></form>
        <p className="muted small">The code changes every 30 seconds. This setup expires in {e.ttl} minutes and only works in this browser.</p></li>
    </ol></PlainCard>
  );
}

/** Everything on Account → Two-factor that an action changes. It sits at one place in the tree and holds each form's answer,
 * so the answer with a seed or recovery codes outlives the page's re-render when the session rotates and 2FA turns on. Each
 * form has its own permalink: without JavaScript, only the form that posted gets the answer. */
export function MfaPanel({ d, csrf }: { d: AccountMfa; csrf: string }) {
  const [begin, beginAct, beginBusy] = useActionState<MfaState | null, FormData>(mfaAct, null, "/account/2fa?f=begin");
  const [confirm, confirmAct, confirmBusy] = useActionState<MfaState | null, FormData>(mfaAct, null, "/account/2fa?f=confirm");
  const [rec, recAct, recBusy] = useActionState<MfaState | null, FormData>(mfaAct, null, "/account/2fa?f=recovery");
  const [off, offAct, offBusy] = useActionState<MfaState | null, FormData>(mfaAct, null, "/account/2fa?f=disable");
  const done = confirm?.done ?? rec?.done;
  if (done) return <CodesSheet c={done} user={d.user} />;
  const enrol = confirm?.enrol ?? begin?.enrol;
  if (enrol) return <EnrolStep e={enrol} st={confirm} act={confirmAct} busy={confirmBusy} csrf={csrf} next={confirm?.next ?? begin?.next ?? d.next} />;
  const reauth = d.reauth ? <><p className="set-note">For your safety, sign in with Keycloak again first (your last sign-in was {d.auth_ago}).</p><p className="row-act"><a data-slot="button" className={buttonVariants()} href={d.reauth}>Sign in with Keycloak again</a></p></> : null;
  if (!d.enabled) {
    const gate = d.reset || d.required;
    return (
      <PlainCard as="section" className="mfa-setup" aria-labelledby="mfa-on-h">
        <h2 id="mfa-on-h">{d.reset ? "Set up two-factor authentication again" : "Turn on two-factor authentication"}</h2>
        <Err st={confirm} />
        {d.reset ? <p className="mfa-lead">An admin reset your two-factor authentication. Your old app and recovery codes no longer work. Set up a new app now: Argus opens no other page until you do.</p>
          : d.required ? <p className="mfa-lead">Argus needs two-factor authentication for every account. Set it up now to continue.</p>
          : <p className="mfa-lead">Argus asks for a second factor before any change, so a leaked password alone cannot change anything.</p>}
        {d.idp && !d.reset ? <p className="set-note">This sign-in used Keycloak&apos;s second factor, which Argus accepts. You can also set up an Argus code, used when Keycloak&apos;s is not available.</p> : null}
        <div className="mfa-need"><h3>You need</h3><p>An app for 6-digit codes on your phone: Google Authenticator, Microsoft Authenticator, 1Password, Aegis or similar.</p></div>
        <ol className="mfa-plan">
          <li>{d.local ? "Confirm your password." : "Start the setup."}</li>
          <li>Scan the QR code with the app.</li>
          <li>Type the code the app shows, then save your recovery codes.</li>
        </ol>
        {reauth ?? (d.key_ready ? <form action={beginAct} className="set-form mfa-go" autoComplete="off"><Hidden csrf={csrf} to="begin" next={d.next} /><Err st={begin} />
          {d.local ? <Password id="mfa-pw" user={d.user} /> : null}
          <p className="row-act"><Button disabled={beginBusy}>{beginBusy ? "Checking…" : "Set up two-factor authentication"}</Button></p></form> : null)}
        {gate ? <p className="muted small">Not now? Use Sign out at the top right.</p> : null}
      </PlainCard>
    );
  }
  const rc = `${d.recovery_left} of ${d.recovery_total} recovery codes left`;
  return (
    <PlainCard as="section" aria-labelledby="mfa-ch-h">
      <dl className="set-kv"><div><dt>Method</dt><dd>Authenticator app</dd></div><div><dt>Last used</dt><dd>{d.last_used ? <When ts={d.last_used} /> : "never"}</dd></div>{d.recovery_left <= 2 ? <div><dt>Recovery codes</dt><dd><Pill tone="warn">{rc}: make new ones below</Pill></dd></div> : null}</dl>
      <div className="card-h"><h2 id="mfa-ch-h">Change something</h2></div><p className="set-note">Each change asks for a current code from your app.</p>
      {reauth ?? <details className="fold mfa-act" open={!!begin}><summary><b>Move to a new phone</b> <span className="muted small">Scan a new QR code; the current app keeps working until the new one is confirmed.</span></summary>
        <form action={beginAct} className="set-form set-stack" autoComplete="off"><Hidden csrf={csrf} to="begin" next={d.next} /><Err st={begin} />{d.local ? <Password id="mfa-pw" user={d.user} /> : null}<CodeField id="mfa-move" />
          <p className="row-act"><Button variant="outline" disabled={beginBusy}>{beginBusy ? "Checking…" : "Show a new QR code"}</Button></p></form></details>}
      <details className="fold mfa-act" open={!!rec}><summary><b>Make new recovery codes</b> <span className="muted small">All your current recovery codes stop working.</span></summary>
        <form action={recAct} className="set-form set-stack" autoComplete="off"><Hidden csrf={csrf} to="recovery" next="/account/2fa" /><Err st={rec} /><CodeField id="mfa-new" /><p className="row-act"><Button variant="outline" disabled={recBusy}>Make new recovery codes</Button></p></form></details>
      <details className="fold mfa-act" open={!!off}><summary><b>Turn off two-factor authentication</b> <span className="muted small">You can still read pages, but you cannot change anything until you turn it on again.</span></summary>
        <form action={offAct} className="set-form set-stack" autoComplete="off"><Hidden csrf={csrf} to="disable" next="/account/2fa" /><Err st={off} />{d.local ? <Password id="mfa-pw-off" user={d.user} /> : null}<CodeField id="mfa-off" /><p className="row-act"><Button variant="destructive" disabled={offBusy}>Turn off two-factor authentication</Button></p></form></details>
    </PlainCard>
  );
}

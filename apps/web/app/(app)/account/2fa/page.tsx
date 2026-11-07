import type { Metadata } from "next";
import { redirect } from "next/navigation";
import { Banner } from "@/components/Banner";
import { MfaPanel } from "@/components/Mfa";
import { Pill } from "@/components/Pills";
import { Banners, Crumb, one, type Search } from "@/components/Section";
import "@/components/mfa.css";
import "@/components/section.css";
import "@/components/settings.css";
import { qs } from "@/lib/api";
import { safeNext } from "@/lib/format";
import { apiGet, csrfToken, readFlash } from "@/lib/server";
import type { AccountMfa } from "@/lib/types";

export const metadata: Metadata = { title: "Two-factor authentication" };

/** Account → Two-factor: set it up (also the sign-in's required enrolment), move to a new phone, new recovery codes, turn it off. */
export default async function MfaPage({ searchParams }: { searchParams: Search }) {
  const next = one((await searchParams).next);
  const [r, csrf, flash] = await Promise.all([apiGet<AccountMfa>("/api/v1/account/2fa" + qs({ next })), csrfToken(), readFlash()]);
  if (r.status === 403) redirect(safeNext((r.data as { next?: string }).next, "/account"));
  if (r.status !== 200) throw new Error(`/api/v1/account/2fa answered ${r.status}`);
  const d = r.data;
  const rc = `${d.recovery_left} of ${d.recovery_total} recovery codes left`;
  const low = d.recovery_left <= 2;
  return (
    <div className="mfa-page">
      <header className="phead"><div>{d.required ? null : <Crumb href="/account" label="Account" />}<h1>Two-factor authentication</h1>
        <p className="psub">{d.enabled ? <><Pill tone="ok">on</Pill> since {d.enabled_at} · {low ? <b>{rc}</b> : rc}</> : d.idp ? <><Pill tone="ok">via Keycloak</Pill> This sign-in used Keycloak&apos;s second factor</> : d.reset ? <><Pill tone="warn">reset by an admin</Pill> Set up a new app to continue</> : <><Pill tone="warn">off</Pill> Needed for every change in Argus</>}</p></div></header>
      <div className="stack">
        <Banners flash={flash} />
        {d.key_ready ? null : <Banner b={{ kind: "fail", text: "Two-factor authentication cannot be set up right now: the key file is missing on the server. Ask the operator." }} />}
        <MfaPanel d={d} csrf={csrf} />
      </div>
    </div>
  );
}

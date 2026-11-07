import type { Metadata } from "next";
import { redirect } from "next/navigation";
import { Banners, Hidden, one, type Search } from "@/components/Section";
import { qs } from "@/lib/api";
import { safeNext } from "@/lib/format";
import { apiGet, csrfToken, readFlash } from "@/lib/server";
import { stepUpAct } from "./actions";
import { Button, buttonVariants } from "@/components/ui/button";
import { PlainCard } from "@/components/ui/card";

export const metadata: Metadata = { title: "Confirm it's you" };

type Stepup = { state: string; why: string; next: string; reauth: string };
const CODE = "[0-9 ]{6,7}|[A-Za-z0-9 -]{16,19}";

/** Where a write that needs a recent code lands: one code, then back to the page the visitor came from. */
export default async function ConfirmPage({ searchParams }: { searchParams: Search }) {
  const s = await searchParams;
  const next = safeNext(one(s.next), "/today");
  const sensitive = one(s.sensitive) === "1" ? "1" : "";
  const [r, csrf, flash] = await Promise.all([apiGet<Stepup>("/api/v1/account/stepup" + qs({ next, sensitive })), csrfToken(), readFlash()]);
  if (r.status !== 200) throw new Error(`/api/v1/account/stepup answered ${r.status}`);
  const d = r.data;
  if (d.state === "enrol") redirect(`/account/2fa?next=${encodeURIComponent(next)}`);
  if (d.state === "") redirect(next);
  return (
    <>
      <header className="phead"><div><h1>Confirm it&apos;s you</h1><p className="psub">Your action was not done yet.</p></div></header>
      <Banners flash={flash} />
      <PlainCard as="section" className="stepup" aria-label="Two-factor code">
        <p>{d.why}{d.reauth ? "" : " Type the code your authenticator app shows now."}</p>
        {d.reauth ? (
          <>
            <p className="muted">Your second factor is checked by your sign-in provider: sign in again, then do the action again.</p>
            <p className="row-act"><a data-slot="button" className={buttonVariants()} href={d.reauth}>Sign in again</a><a data-slot="button" className={buttonVariants({ variant: "outline" })} href={next}>Back to the page</a></p>
          </>
        ) : (
          <>
            <form action={stepUpAct} className="stack-f">
              <Hidden csrf={csrf} next={next} sensitive={sensitive} />
              <label className="field"><span>6-digit code from your app</span>
                <input name="code" inputMode="numeric" autoComplete="one-time-code" pattern={CODE} maxLength={19} required spellCheck={false} autoCapitalize="off" autoFocus /></label>
              <p className="row-act"><Button>Confirm</Button><a data-slot="button" className={buttonVariants({ variant: "ghost" })} href={next}>Back to the page</a></p>
            </form>
            <details className="fold"><summary>Lost your phone? Use a recovery code</summary>
              <form action={stepUpAct} className="stack-f">
                <Hidden csrf={csrf} next={next} sensitive={sensitive} />
                <label className="field"><span>Recovery code</span>
                  <input name="code" inputMode="text" autoComplete="off" pattern={CODE} maxLength={19} required spellCheck={false} autoCapitalize="off" placeholder="xxxx-xxxx-xxxx-xxxx" /></label>
                <p><Button variant="outline">Use this recovery code</Button></p>
              </form>
              <p className="muted small">Each recovery code works once. No codes left? Ask another admin to reset your two-factor authentication.</p>
            </details>
          </>
        )}
      </PlainCard>
    </>
  );
}

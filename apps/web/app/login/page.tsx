import type { Metadata } from "next";
import { redirect } from "next/navigation";
import { Banner } from "@/components/Banner";
import { Mark } from "@/components/Icon";
import { ThemeSwitch } from "@/components/ThemeSwitch";
import { safeNext } from "@/lib/format";
import { apiGet, csrfToken, readFlash } from "@/lib/server";
import type { AuthOptions } from "@/lib/types";
import { login, loginCode } from "./actions";
import "./login.css";
import { Button, buttonVariants } from "@/components/ui/button";
import { cn } from "cn";

export const metadata: Metadata = { title: "Sign in" };

type Search = Promise<{ next?: string; step?: string; sso?: string }>;

/** What the API's /auth/callback sends back: a code, so no free text can be put on this page by a link. */
const SSO_REFUSED: Record<string, string> = {
  unavailable: "Single sign-on cannot be reached right now. Try again in a minute, or use a local account.",
  incomplete: "Single sign-on did not complete. Try again.",
  username: "Your single sign-on user name cannot be used as an account name here (lowercase letters, digits, dot, dash, underscore).",
  norole: "Your single sign-on account has no role here. Ask an admin for the client role admin or viewer.",
  denied: "Sign-in with this account is not allowed. Ask an admin.",
  unsaved: "The sign-in could not be saved. Try again.",
};

export default async function LoginPage({ searchParams }: { searchParams: Search }) {
  const sp = await searchParams;
  const next = safeNext(sp.next);
  const [me, opts, pending] = await Promise.all([
    apiGet("/api/v1/me"),
    apiGet<AuthOptions>("/api/v1/auth/options"),
    sp.step === "code" ? apiGet<{ pending: boolean; username?: string }>("/api/v1/auth/mfa") : Promise.resolve(null),
  ]);
  if (me.status === 200) redirect(next);
  const o = opts.data;
  const csrf = await csrfToken();
  const flash = await readFlash();
  const codeStep = !!pending?.data.pending;
  const ssoRefused = Object.hasOwn(SSO_REFUSED, sp.sso ?? "") ? SSO_REFUSED[sp.sso as string] : "";

  return (
    <div className="auth">
      {o.demo && o.demoBanner ? <p className="demo-strip" role="note"><strong>Demo</strong> {o.demoBanner}</p> : null}
      <main id="main" tabIndex={-1} className="auth-main">
        <div className="auth-card">
          <div className="auth-brand">
            <Mark size={32} />
            <span><b>Argus</b><small>{o.subtitle}</small></span>
          </div>
          {codeStep ? (
            <>
              <h1>Enter your code</h1>
              <p className="auth-sub">Signed in as <b>{pending?.data.username}</b>. Open your authenticator app and type the 6-digit code, or use a recovery code.</p>
              {flash ? <Banner b={flash} /> : null}
              <form action={loginCode} className="auth-form">
                <input type="hidden" name="csrf" value={csrf} />
                <input type="hidden" name="next" value={next} />
                <label className="field">
                  <span>Code</span>
                  <input name="code" inputMode="numeric" autoComplete="one-time-code" required maxLength={32} autoFocus spellCheck={false} />
                </label>
                <Button className="w-full">Verify and sign in</Button>
              </form>
              <form action={loginCode}>
                <input type="hidden" name="csrf" value={csrf} />
                <input type="hidden" name="next" value={next} />
                <Button variant="ghost" className="w-full" name="action" value="cancel">Cancel and start again</Button>
              </form>
            </>
          ) : (
            <>
              <h1>Sign in</h1>
              <p className="auth-sub">The operations console for {o.subtitle || "your team"}.</p>
              {flash ? <Banner b={flash} /> : null}
              {ssoRefused ? <Banner b={{ kind: "fail", text: ssoRefused }} /> : null}
              {o.demo && o.demoUsers?.length ? (
                <div className="demo-login">
                  {o.demoUsers.map((u, i) => (
                    <form key={u.username} action={login}>
                      <input type="hidden" name="csrf" value={csrf} />
                      <input type="hidden" name="next" value={next} />
                      <input type="hidden" name="username" value={u.username} />
                      <input type="hidden" name="password" value={o.demoPassword ?? ""} />
                      <Button variant={i === 0 ? "default" : "outline"} className="w-full">{u.label}</Button>
                    </form>
                  ))}
                  <p className="hint">Sample data of a fictional company. Nothing you click changes anything.</p>
                </div>
              ) : null}
              {o.passwordLogin ? (
                <form action={login} className="auth-form">
                  {o.demo ? <p className="or"><span>or with an account</span></p> : null}
                  <input type="hidden" name="csrf" value={csrf} />
                  <input type="hidden" name="next" value={next} />
                  <label className="field">
                    <span>Username</span>
                    <input name="username" autoComplete="username" required maxLength={64} spellCheck={false} autoCapitalize="off" />
                  </label>
                  <label className="field">
                    <span>Password</span>
                    <input name="password" type="password" autoComplete="current-password" required maxLength={256} />
                  </label>
                  <label className="check">
                    <input type="checkbox" name="remember" value="1" /> <span>Keep me signed in on this device</span>
                  </label>
                  <Button variant={o.demo ? "outline" : "default"} className="w-full">Sign in</Button>
                </form>
              ) : null}
              {o.sso ? (
                <a data-slot="button" className={cn(buttonVariants({ variant: "outline" }), "w-full")} href={`/auth/login?next=${encodeURIComponent(next)}`}>Sign in with single sign-on</a>
              ) : null}
            </>
          )}
        </div>
        <footer className="auth-foot">
          <span>{o.product} · {o.subtitle}</span>
          <ThemeSwitch compact />
        </footer>
      </main>
    </div>
  );
}

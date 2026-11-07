import { cookies } from "next/headers";
import { logout } from "@/app/actions";
import { Commands } from "@/components/Commands";
import { Navigate } from "@/components/Soft";
import "@/components/settings.css";
import { RAIL_COOKIE } from "@/lib/names";
import { csrfToken, currentUrl, shellMe, shellNav } from "@/lib/server";
import "./shell.css";
import { Button } from "@/components/ui/button";

// The app shell (DESIGN.md 3.1): sidebar | top bar over the page. Every page under it needs a session. The sidebar and the
// top bar are the slots `side` and `top` (components/Shell.tsx): they follow a client navigation, this layout does not.
export default async function AppLayout({ children, side, top }: { children: React.ReactNode; side: React.ReactNode; top: React.ReactNode }) {
  const me = await shellMe();
  const url = await currentUrl();
  const path = url.split("?")[0] ?? "/";
  // No shell for a session that must change its password or enrol first: the API refuses what the shell would show. None on
  // Two-factor either: turning it on must not change the tree under the form that holds the recovery codes.
  if (me.mustChangePassword || me.mfa.enrolRequired || path === "/account/2fa") {
    return (
      <>
        <div className="bare-top"><form action={logout}><input type="hidden" name="csrf" value={await csrfToken()} /><Button variant="ghost" size="sm">Sign out</Button></form></div>
        <main id="main" tabIndex={-1} className="page">{children}</main>
      </>
    );
  }
  const nav = await shellNav();
  const rail = (await cookies()).get(RAIL_COOKIE)?.value === "on";
  return (
    <>
      {me.demo && me.demoBanner ? <p className="demo-strip" role="note"><strong>Demo</strong> {me.demoBanner}</p> : null}
      <div className="shell" data-rail={rail ? "on" : undefined}>
        {side}
        <div className="canvas">
          {top}
          <main id="main" tabIndex={-1} className="page">
            {children}
          </main>
        </div>
      </div>
      <Commands nav={nav.data} admin={me.role === "admin"} />
      <Navigate />
    </>
  );
}

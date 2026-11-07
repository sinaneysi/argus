import { cookies } from "next/headers";
import { RAIL_COOKIE } from "@/lib/names";
import { currentUrl, shellMe, shellNav, shellStatus } from "@/lib/server";
import { Sidebar } from "./Sidebar";
import { TopBar } from "./TopBar";

// The shell's per-page parts. They are the layout's parallel route slots (app/(app)/@side, @top), not part of the layout
// itself: a layout is not re-rendered by a client navigation, a slot whose segment changes is, so the current page, its
// open group, the counts, the crumb and the freshness are right after next/link as after a document load.

async function pathNow(): Promise<string> {
  return (await currentUrl()).split("?")[0] || "/";
}

export async function ShellSide() {
  const [me, nav, path, jar] = await Promise.all([shellMe(), shellNav(), pathNow(), cookies()]);
  return <Sidebar me={me} nav={nav.data} path={path} rail={jar.get(RAIL_COOKIE)?.value === "on"} />;
}

export async function ShellTop() {
  const [nav, status, path] = await Promise.all([shellNav(), shellStatus(), pathNow()]);
  return <TopBar nav={nav.data} status={status.status === 200 ? status.data : null} path={path} />;
}

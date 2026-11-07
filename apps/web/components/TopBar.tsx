import { ago, clock, full, iso } from "@/lib/format";
import type { Nav, Status } from "@/lib/types";
import { Icon } from "./Icon";
import { Keys } from "./Keys";
import { currentOf } from "./Sidebar";
import { Button, buttonVariants } from "@/components/ui/button";
import { cn } from "cn";

const STATE_WORD = { ok: "", stale: "Stale", failed: "", unknown: "No data yet" } as const;

/** What is wrong, in words: the task's name when it is one, the count when there are more. */
function failedWord(failed: string[]): string {
  if (failed.length === 1) return `${failed[0]} not OK`;
  return failed.length ? `${failed.length} tasks not OK` : "Tasks not OK";
}

/** "Data as of 09:42 · checks 09:40" with a dot: ok, stale (amber), failed (red). With a failure it names what failed and links to it. */
export function Freshness({ s }: { s: Status | null }) {
  if (!s) return <span className="fresh unknown">Data status unavailable</span>;
  const bad = s.state === "failed";
  const word = bad ? failedWord(s.failed) : STATE_WORD[s.state];
  const title = s.dataAsOf
    ? `Collectors last finished ${full(s.dataAsOf)} (${ago(s.dataAsOf, s.now)})${s.failed.length ? "; failed: " + s.failed.join(", ") : ""}. Page rendered ${full(s.now)}.`
    : "No collector has run yet.";
  return (
    <a className={`fresh ${s.state}`} href={bad ? s.failedHref : "/runs"} title={title}>
      <span className="fresh-dot" aria-hidden="true" />
      {word ? <b>{word}</b> : null}
      {s.dataAsOf ? (
        <span>Data as of <time dateTime={iso(s.dataAsOf)}>{clock(s.dataAsOf)}</time></span>
      ) : null}
      {s.checksAt && !bad ? <span className="fresh-x"> · checks <time dateTime={iso(s.checksAt)}>{clock(s.checksAt)}</time></span> : null}
      {bad ? null : <span className="fresh-x"> · refreshed <time dateTime={iso(s.now)}>{clock(s.now)}</time></span>}
    </a>
  );
}

// The top bar (DESIGN.md 3.3): breadcrumb and the freshness pill (search sits in the sidebar); on phones the menu button.
export function TopBar({ nav, status, path }: { nav: Nav; status: Status | null; path: string }) {
  const cur = currentOf(nav, path);
  return (
    <header className="topbar">
      <a data-slot="button" className={cn(buttonVariants({ variant: "ghost", size: "icon" }), "menu-btn")} id="menu" href="#nav"><Icon name="menu" /><span className="sr">Open the menu</span></a>
      <nav className="crumb" aria-label="Breadcrumb">
        <ol>
          {cur.group && cur.group.name !== cur.label ? <li className="crumb-g">{cur.group.name}</li> : null}
          <li aria-current="page">{cur.label ?? "Argus"}</li>
        </ol>
      </nav>
      <Button type="button" variant="ghost" size="sm" className="cmdk-open" data-cmdk-open aria-label="Search or jump"><Icon name="search" /><span className="cmdk-word">Search or jump</span> <Keys k="Ctrl K" /></Button>
      <Freshness s={status} />
    </header>
  );
}

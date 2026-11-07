import { cn } from "cn";
import { logout, setRail } from "@/app/actions";
import { csrfToken, currentUrl } from "@/lib/server";
import type { Me, Nav, NavGroup } from "@/lib/types";
import { Icon, Mark } from "./Icon";
import { ThemeSwitch } from "./ThemeSwitch";
import { Button } from "@/components/ui/button";

const GROUP_ICON: Record<string, string> = {
  home: "home", incidents: "warn", infrastructure: "server", delivery: "branch", work: "layers", agent: "bot", marketing: "megaphone",
};
// One glyph per page and per group: the rail shows icons alone, and a page must not repeat its group's icon.
const LINK_ICON: Record<string, string> = {
  "/today": "sun", "/review": "inbox", "/incidents": "activity", "/alerts": "bell", "/problems": "target", "/rca": "book", "/inventory": "box", "/capacity": "gauge", "/certs": "lock",
  "/backups": "database", "/operations": "shield-check", "/hygiene": "filter", "/cicd": "workflow", "/cicd/repo": "search", "/vulns": "shield-alert",
  "/board": "kanban", "/qa": "message", "/board/new": "plus", "/runs": "terminal", "/knowledge": "bulb", "/admin/fix": "wrench",
  "/seo": "trending",
};

/** The group and link of a path: the link whose page is the path, else the one whose page starts it. */
export function currentOf(nav: Nav, path: string): { group?: NavGroup; label?: string } {
  for (const g of nav.groups) for (const l of g.links) if (l.href === path) return { group: g, label: l.label };
  for (const g of nav.groups) for (const l of g.links) if (path.startsWith(l.href + "/")) return { group: g, label: l.label };
  return {};
}

// .side-link is in app/tailwind.css: about 30 rows carry it, and every class string ships twice, in the HTML and in the RSC payload.

function Count({ n, title, alarm, sum }: { n: number; title: string; alarm?: boolean; sum?: boolean }) {
  return (
    <>
      <span className={cn("ncount h-5 min-w-5 px-1.5 rail:hidden", alarm && "alarm", sum && "group-open/ng:hidden")} aria-hidden="true" title={title}>{n}</span>
      <span className={cn("sr", sum && "group-open/ng:hidden")}>, {title}</span>
      <span className={cn("absolute top-1.5 right-2.5 hidden size-2 rounded-full rail:block", alarm ? "bg-fail-foreground" : "bg-primary", sum && "group-open/ng:hidden")} aria-hidden="true" />
    </>
  );
}

// The sidebar (DESIGN.md 3.2): six groups and Marketing as disclosures (the current page's group open), then Help, Settings, the
// theme switch and the user. It stays a server component: the groups are <details>, the phone sheet opens through
// #nav (:target), and the icon rail is a cookie set by a form, so all three work without JavaScript. On the rail every
// group is open with its summary hidden, so each page keeps its icon; the `rail:` variant is in app/tailwind.css.
export async function Sidebar({ me, nav, path, rail }: { me: Me; nav: Nav; path: string; rail: boolean }) {
  const cur = currentOf(nav, path);
  const [csrf, back] = await Promise.all([csrfToken(), currentUrl()]);
  return (
    <aside
      id="nav"
      aria-label="Sidebar"
      data-slot="sidebar"
      className="sticky top-(--s3) flex h-[calc(100vh-2*var(--s3))] flex-col gap-2 overflow-x-hidden overflow-y-auto overscroll-contain rounded-xl bg-card px-3 pt-4 pb-3 shadow-card rail:px-2.5 max-[900px]:invisible max-[900px]:fixed max-[900px]:inset-y-0 max-[900px]:left-0 max-[900px]:z-20 max-[900px]:h-auto max-[900px]:w-[min(320px,88vw)] max-[900px]:-translate-x-full max-[900px]:rounded-l-none max-[900px]:shadow-overlay max-[900px]:transition-[translate,visibility] max-[900px]:duration-(--t-overlay) max-[900px]:ease-harbor max-[900px]:target:visible max-[900px]:target:translate-x-0"
    >
      <div className="flex items-center justify-between px-1.5 pt-1 pb-3 rail:justify-center rail:px-0">
        <a data-slot="sidebar-brand" className="flex items-center gap-2.5 text-foreground" href="/today" aria-label="Argus: Today">
          <Mark size={28} />
          <span className="rail:hidden"><b className="block text-lg font-semibold">Argus</b><small className="block text-xs text-muted-foreground">{me.subtitle}</small></span>
        </a>
        <a data-slot="sidebar-close" className="hidden size-11 items-center justify-center text-ink-2 max-[900px]:inline-flex" href="#menu">
          <Icon name="x" /><span className="sr">Close the menu</span>
        </a>
      </div>
      <form className="rail:hidden" method="get" action={nav.search.action} role="search" data-side-search>
        {Object.entries(nav.search.hidden).map(([k, v]) => <input key={k} type="hidden" name={k} value={v} />)}
        <label className="flex h-9 w-full items-center gap-2 rounded-[8px] bg-secondary px-2.5 text-muted-foreground focus-within:outline-2 focus-within:outline-offset-2 focus-within:outline-(--focus)">
          <Icon name="search" />
          <span className="sr">Search</span>
          <input
            className="w-full min-w-0 flex-1 border-0 bg-transparent text-sm text-ellipsis text-foreground outline-none [&::-webkit-search-cancel-button]:hidden"
            type="search" name={nav.search.param} placeholder={nav.search.placeholder} maxLength={100} autoComplete="off" spellCheck={false}
          />
        </label>
      </form>
      <nav className="grid gap-1.5" aria-label="Main">
        {nav.groups.map((g) => {
          const open = cur.group?.key === g.key;
          const sum = g.links.reduce((s, l) => s + (l.count ?? 0), 0);
          return (
            <details key={g.key} className="group/ng rail:not-last:border-b rail:not-last:pb-1.5" open={open || rail || undefined}>
              <summary className={cn("flex h-10 cursor-pointer list-none items-center gap-1.5 rounded-[8px] px-2.5 text-sm font-semibold pointer-coarse:min-h-11 hover:bg-accent hover:text-foreground [&::-webkit-details-marker]:hidden rail:hidden", open ? "text-soft-foreground" : "text-ink-2")}>
                <Icon name="chevron" size={16} className="text-muted-foreground transition-transform duration-(--t) ease-harbor group-open/ng:rotate-90" />
                <Icon name={GROUP_ICON[g.key] ?? "dot"} size={18} className="mr-1" />
                <span className="min-w-0 flex-1 truncate">{g.name}</span>
                {open ? <span className="sr">, current group</span> : null}
                {sum > 0 ? <Count n={sum} title={`${sum} waiting in ${g.name}`} sum alarm={g.links.some((l) => l.alarm)} /> : null}
              </summary>
              <ul className="grid gap-0.5 pt-0.5" aria-label={g.name}>
                {g.links.map((l) => {
                  const on = open && cur.label === l.label;
                  return (
                    <li key={l.href}>
                      <a
                        data-slot="sidebar-link"
                        href={l.href}
                        title={l.label}
                        className={on ? "side-link side-link-on" : "side-link"}
                        aria-current={on ? (path === l.href ? "page" : "true") : undefined}
                      >
                        <Icon name={LINK_ICON[l.href] ?? "dot"} />
                        <span className="min-w-0 flex-1 truncate rail:sr-only">{l.label}</span>
                        {l.count ? <Count n={l.count} title={l.countTitle || `${l.count} waiting`} alarm={l.alarm} /> : null}
                      </a>
                    </li>
                  );
                })}
              </ul>
            </details>
          );
        })}
      </nav>
      <div className="mt-auto grid gap-2.5 border-t border-border pt-3">
        <ul>
          {nav.footer.map((l) => (
            <li key={l.href}>
              <a data-slot="sidebar-link" href={l.href} title={l.label} className="side-link font-medium">
                <Icon name={l.label === "Help" ? "help" : "settings"} /><span className="rail:sr-only">{l.label}</span>
              </a>
            </li>
          ))}
          <li className="max-[900px]:hidden">
            <form action={setRail}>
              <input type="hidden" name="back" value={back} />
              <button name="rail" value={rail ? "off" : "on"} title={rail ? "Expand the sidebar" : "Collapse the sidebar"} className="side-link w-full font-medium">
                <Icon name="panel" /><span className="rail:sr-only">{rail ? "Expand" : "Collapse"}</span>
              </button>
            </form>
          </li>
        </ul>
        <div className="rail:hidden"><ThemeSwitch /></div>
        <div className="flex items-center gap-2.5 pt-1 pr-1 pl-2 rail:flex-col rail:px-0">
          <span className="grid size-7 flex-none place-items-center rounded-full bg-secondary text-xs font-semibold text-ink-2" aria-hidden="true">{me.username.slice(0, 2).toUpperCase()}</span>
          <span className="grid min-w-0 flex-1 text-sm rail:sr-only">
            <a data-slot="sidebar-user" className="truncate font-medium text-foreground hover:underline" href="/account" title="Account">{me.username}</a>
            <small className="text-xs text-muted-foreground">{me.role}</small>
          </span>
          <form action={logout}>
            <input type="hidden" name="csrf" value={csrf} />
            <Button variant="ghost" size="icon" title="Sign out" aria-label="Sign out"><Icon name="logout" /></Button>
          </form>
        </div>
      </div>
    </aside>
  );
}

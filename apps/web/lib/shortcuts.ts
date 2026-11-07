import type { Nav } from "./types";

// Keyboard shortcuts and the command palette's content (DESIGN.md 3.23). The commands come from /api/v1/nav, so a
// role sees only the pages it has.

/** The second key of each "g then key" jump. */
export const GO_KEYS: Record<string, string> = {
  "/today": "t", "/review": "r", "/incidents": "i", "/alerts": "a", "/problems": "p", "/operations": "c", "/inventory": "n",
  "/certs": "s", "/backups": "k", "/cicd": "d", "/vulns": "v", "/board": "b", "/qa": "q", "/runs": "u", "/knowledge": "w",
};

export const SEARCH_WORDS: Record<string, string[]> = {
  "/today": ["plan", "needs you"], "/review": ["decisions", "approve", "proposals"], "/incidents": ["outage", "broken"],
  "/operations": ["health"], "/inventory": ["vm", "host", "ip"], "/certs": ["ssl", "tls", "expiry"], "/backups": ["restore", "dump"],
  "/cicd": ["pipeline", "deploy", "delivery"], "/vulns": ["cve", "kev"], "/board": ["kanban", "jira", "tasks"], "/qa": ["chat"],
  "/runs": ["agent", "collector"], "/knowledge": ["lessons", "wiki"],
};

export type GoCommand = { label: string; href: string; group: string; keys?: string; words: string[] };

/** Every page in the nav: those with a jump first, in the order of the nav. */
export function goCommands(nav: Nav): GoCommand[] {
  const all: GoCommand[] = [];
  for (const g of nav.groups) {
    for (const l of g.links) {
      const k = GO_KEYS[l.href];
      all.push({ label: l.label, href: l.href, group: g.key, keys: k ? `g ${k}` : undefined, words: [g.name, ...(SEARCH_WORDS[l.href] ?? [])] });
    }
  }
  for (const l of nav.footer) all.push({ label: l.label, href: l.href, group: "footer", words: [] });
  return all;
}

export type ActionId = "new-task" | "ask" | "find" | "replan" | "theme" | "help" | "toggle";
export type ActionCommand = { id: ActionId; label: string; icon: string; keys?: string };

export function actionCommands(nav: Nav, admin: boolean, singleKeys: boolean): ActionCommand[] {
  const has = (href: string) => nav.groups.some((g) => g.links.some((l) => l.href === href));
  const a: ActionCommand[] = [];
  if (has("/board/new")) a.push({ id: "new-task", label: "New task", icon: "list", keys: "c" });
  if (has("/board")) a.push({ id: "ask", label: "Ask Argus", icon: "terminal", keys: "a" });
  a.push({ id: "find", label: "Find a VM", icon: "search", keys: "/" });
  if (admin) a.push({ id: "replan", label: "Re-plan today", icon: "reset", keys: "Shift R" });
  a.push({ id: "theme", label: "Switch light or dark", icon: "moon", keys: "Shift T" });
  a.push({ id: "help", label: "Show keyboard shortcuts", icon: "help", keys: "?" });
  a.push({ id: "toggle", label: singleKeys ? "Turn single-key shortcuts off" : "Turn single-key shortcuts on", icon: "settings" });
  return a;
}

/** The key as the shortcut tables name it: "Shift R" for a capital, else the key itself. */
export function keyName(key: string, shift: boolean): string {
  return shift && key.length === 1 && key >= "A" && key <= "Z" ? `Shift ${key}` : key;
}

/** The address of a free search in the palette: the same form the sidebar search sends. */
export function searchHref(nav: Nav, q: string): string {
  const u = new URLSearchParams(nav.search.hidden);
  u.set(nav.search.param, q);
  return `${nav.search.action}?${u}`;
}

export const ROWS = ".finding, .q-row, .l-table tbody tr, .kcard";
export const SHORTCUTS_OFF = "argus-shortcuts-off";

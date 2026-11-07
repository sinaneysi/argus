"use client";

import { useEffect, useMemo, useState } from "react";
import { searchHref, type ActionCommand, type ActionId, type GoCommand } from "@/lib/shortcuts";
import type { Nav } from "@/lib/types";
import { Icon } from "./Icon";
import { Keys } from "./Keys";
import { Command, CommandGroup, CommandInput, CommandItem, CommandList } from "./ui/command";
import { Dialog, DialogContent, DialogTitle } from "./ui/dialog";

const GROUP_ICON: Record<string, string> = {
  home: "home", incidents: "activity", infrastructure: "server", delivery: "branch", work: "list", agent: "terminal", footer: "settings",
};

type Incident = { id: number; title: string; status?: string; resolved?: boolean };
type Entry = { id: string; label: string; icon: string; keys?: string; words: string[]; href?: string; action?: ActionId };

/** Every word typed must appear in the label or the keywords; an empty search shows everything. */
function hit(e: Entry, search: string): boolean {
  const text = [e.label, ...e.words].join(" ").toLowerCase();
  return search.toLowerCase().split(/\s+/).filter(Boolean).every((w) => text.includes(w));
}

/** The command palette: cmdk in a Dialog. Loaded on the first Ctrl K. Open incidents come from the API when it opens. */
export default function Palette({ open, onOpen, nav, go, actions, run }: {
  open: boolean; onOpen: (on: boolean) => void; nav: Nav; go: GoCommand[]; actions: ActionCommand[]; run: (id: ActionId) => void;
}) {
  const [search, setSearch] = useState("");
  const [incidents, setIncidents] = useState<Incident[]>([]);

  useEffect(() => {
    if (!open) return;
    const c = new AbortController();
    fetch("/api/v1/incidents", { signal: c.signal, credentials: "same-origin" })
      .then((r) => (r.ok ? r.json() : null))
      .then((d: { rows?: Incident[] } | null) => setIncidents((d?.rows ?? []).filter((x) => !x.resolved).slice(0, 8)))
      .catch(() => { /* the palette works without them */ });
    return () => c.abort();
  }, [open]);

  const groups = useMemo(() => {
    const page = (c: GoCommand): Entry => ({ id: c.href, label: c.label, icon: GROUP_ICON[c.group] ?? "dot", keys: c.keys, words: c.words, href: c.href });
    const all: { heading: string; items: Entry[] }[] = [
      { heading: "Actions", items: actions.map((a) => ({ id: a.id, label: a.label, icon: a.icon, keys: a.keys, words: [], action: a.id })) },
      { heading: "Go to", items: go.filter((c) => c.keys).map(page) },
      { heading: "Open incidents", items: incidents.map((x) => ({ id: `incident-${x.id}`, label: `#${x.id} ${x.title}`, icon: "activity", words: [], href: `/incidents/view?id=${x.id}` })) },
      { heading: "More pages", items: go.filter((c) => !c.keys).map(page) },
    ];
    return all.map((g) => ({ ...g, items: g.items.filter((e) => hit(e, search)) })).filter((g) => g.items.length);
  }, [actions, go, incidents, search]);

  const change = (on: boolean) => { onOpen(on); if (!on) setSearch(""); };
  const pick = (e: Entry) => {
    change(false);
    if (e.href) location.assign(e.href);
    else if (e.action) run(e.action);
  };

  return (
    <Dialog open={open} onOpenChange={change}>
      <DialogContent aria-label="Command palette">
        <DialogTitle className="sr">Command palette</DialogTitle>
        <Command shouldFilter={false} loop label="Command palette">
          <CommandInput autoFocus value={search} onValueChange={setSearch} placeholder="Search pages, actions and incidents" />
          <CommandList>
            {groups.map((g) => (
              <CommandGroup key={g.heading} heading={g.heading}>
                {g.items.map((e) => (
                  <CommandItem key={e.id} value={e.id} onSelect={() => pick(e)}>
                    <span className="inline-flex text-link"><Icon name={e.icon} size={18} /></span>
                    <span className="min-w-0 flex-1 truncate" dir="auto">{e.label}</span>
                    {e.keys ? <Keys k={e.keys} /> : null}
                  </CommandItem>
                ))}
              </CommandGroup>
            ))}
            {groups.length ? null : (
              <CommandItem value="search" onSelect={() => { change(false); location.assign(searchHref(nav, search)); }}>
                <span className="inline-flex text-link"><Icon name="search" size={18} /></span>
                <span className="min-w-0 flex-1 truncate" dir="auto">Search Inventory for “{search}”</span>
              </CommandItem>
            )}
          </CommandList>
        </Command>
        <p className="flex items-center gap-1.5 border-t px-3.5 py-2 text-sm text-muted-foreground [&>.keys:not(:first-child)]:ml-2"><Keys k="↑ ↓" /> move <Keys k="Enter" /> run <Keys k="Esc" /> close</p>
      </DialogContent>
    </Dialog>
  );
}

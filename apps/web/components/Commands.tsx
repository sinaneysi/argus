"use client";

import dynamic from "next/dynamic";
import { useCallback, useEffect, useMemo, useRef, useState } from "react";
import { actionCommands, goCommands, keyName, ROWS, SHORTCUTS_OFF, type ActionId } from "@/lib/shortcuts";
import type { Nav } from "@/lib/types";
import "./commands.css";

// The palette and its library load on the first Ctrl K, so no page pays for them before that.
const Palette = dynamic(() => import("./Palette"), { ssr: false });
const ShortcutHelp = dynamic(() => import("./ShortcutHelp"), { ssr: false });

const JUMP_MS = 1200;

function typing(t: EventTarget | null): boolean {
  return t instanceof HTMLElement && (t.isContentEditable || /^(INPUT|TEXTAREA|SELECT)$/.test(t.tagName));
}

function moveRow(step: number) {
  const rows = Array.from(document.querySelectorAll<HTMLElement>(ROWS)).filter((r) => r.getClientRects().length);
  if (!rows.length) return;
  const i = rows.findIndex((r) => r.classList.contains("kb-row"));
  rows[i]?.classList.remove("kb-row");
  const next = rows[Math.min(rows.length - 1, Math.max(0, i + step))]!;
  next.classList.add("kb-row");
  next.scrollIntoView({ block: "nearest" });
}

/** Ctrl K, the g jumps, the single-key actions and the shortcut list (DESIGN.md 3.23). Mounted once in the app shell. */
export function Commands({ nav, admin }: { nav: Nav; admin: boolean }) {
  const [open, setOpen] = useState(false);
  const [loaded, setLoaded] = useState(false);
  const [help, setHelpOpen] = useState(false);
  const [helpLoaded, setHelpLoaded] = useState(false);
  const [singleKeys, setSingleKeys] = useState(true);
  const go = useMemo(() => goCommands(nav), [nav]);
  const actions = useMemo(() => actionCommands(nav, admin, singleKeys), [nav, admin, singleKeys]);

  useEffect(() => {
    try { setSingleKeys(localStorage.getItem(SHORTCUTS_OFF) !== "1"); } catch { /* storage blocked: keep the default */ }
  }, []);

  const showPalette = useCallback((on: boolean) => {
    if (on) setLoaded(true);
    setOpen(on);
  }, []);

  const setHelp = useCallback((on: boolean) => {
    if (on) setHelpLoaded(true);
    setHelpOpen(on);
  }, []);

  const run = useCallback((id: ActionId) => {
    switch (id) {
      case "new-task": location.assign("/board/new"); break;
      case "ask": location.assign("/board/ask"); break;
      case "find": {
        const input = document.querySelector<HTMLInputElement>("[data-side-search] input[type=search]");
        if (input?.getClientRects().length) input.focus(); else location.assign(nav.search.action);
        break;
      }
      case "replan": {
        const b = document.querySelector<HTMLButtonElement>("[data-cmd=replan] button");
        if (!b) location.assign("/today");
        else if (confirm("Re-plan today? Argus collects everything again and refills the day.")) b.click();
        break;
      }
      case "theme": {
        const dark = document.documentElement.dataset.theme === "dark" || (!document.documentElement.dataset.theme && matchMedia("(prefers-color-scheme: dark)").matches);
        document.querySelector<HTMLButtonElement>(`.theme button[value=${dark ? "light" : "dark"}]`)?.click();
        break;
      }
      case "help": setHelp(true); break;
      case "toggle": setSingleKeys((on) => {
        try { if (on) localStorage.setItem(SHORTCUTS_OFF, "1"); else localStorage.removeItem(SHORTCUTS_OFF); } catch { /* storage blocked: this page only */ }
        return !on;
      }); break;
    }
  }, [nav.search.action, setHelp]);

  const live = useRef({ go, actions, run, singleKeys, open });
  live.current = { go, actions, run, singleKeys, open };

  useEffect(() => {
    let jumpAt = 0;
    const onKey = (e: KeyboardEvent) => {
      if ((e.ctrlKey || e.metaKey) && !e.altKey && e.key.toLowerCase() === "k") {
        e.preventDefault();
        showPalette(!live.current.open);
        return;
      }
      const s = live.current;
      if (!s.singleKeys || s.open || e.ctrlKey || e.metaKey || e.altKey || typing(e.target)) return;
      if (jumpAt && Date.now() - jumpAt < JUMP_MS) {
        jumpAt = 0;
        const hit = s.go.find((c) => c.keys === `g ${e.key}`);
        if (hit) { e.preventDefault(); location.assign(hit.href); }
        return;
      }
      if (e.key === "g") { jumpAt = Date.now(); return; }
      const name = keyName(e.key, e.shiftKey);
      const act = s.actions.find((a) => a.keys === name);
      if (act) { e.preventDefault(); s.run(act.id); return; }
      if (e.key === "j") moveRow(1);
      else if (e.key === "k") moveRow(-1);
      else if (e.key === "Enter") document.querySelector<HTMLElement>(".kb-row a")?.click();
    };
    const onClick = (e: MouseEvent) => {
      if (e.target instanceof Element && e.target.closest("[data-cmdk-open]")) showPalette(true);
    };
    document.addEventListener("keydown", onKey);
    document.addEventListener("click", onClick);
    return () => { document.removeEventListener("keydown", onKey); document.removeEventListener("click", onClick); };
  }, [showPalette]);

  return (
    <>
      {loaded ? <Palette open={open} onOpen={showPalette} nav={nav} go={go} actions={actions} run={run} /> : null}
      {helpLoaded ? <ShortcutHelp open={help} onOpen={setHelp} go={go} actions={actions} singleKeys={singleKeys} /> : null}
    </>
  );
}

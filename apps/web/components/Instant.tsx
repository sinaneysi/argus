"use client";

import { useEffect, useRef, useState } from "react";
import { RAIL_COOKIE } from "@/lib/names";

// UI preferences that change at once in the browser. Each is still a plain form with a server action, so it works
// without JavaScript; with JavaScript the click is handled here and the page neither reloads nor scrolls.

type Action = (fd: FormData) => Promise<void>;

/** Collapse / Expand: flips data-rail on .shell and writes the cookie here; no request. The form is the no-JS fallback. */
export function RailToggle({ rail, back, action, children }: { rail: boolean; back: string; action: Action; children: React.ReactNode }) {
  const [on, setOn] = useState(rail);
  useEffect(() => setOn(rail), [rail]);
  function flip(e: React.MouseEvent<HTMLButtonElement>) {
    const shell = e.currentTarget.closest(".shell");
    if (!shell) return; // not handled: the form posts
    e.preventDefault();
    const next = !on;
    if (next) shell.setAttribute("data-rail", "on");
    else shell.removeAttribute("data-rail");
    const secure = location.protocol === "https:" ? "; secure" : "";
    document.cookie = next ? `${RAIL_COOKIE}=on; path=/; max-age=31536000; samesite=lax${secure}` : `${RAIL_COOKIE}=; path=/; max-age=0; samesite=lax${secure}`;
    // On the rail every group is open (its icons are the links); expanded, only the current page's group stays open.
    for (const d of shell.querySelectorAll<HTMLDetailsElement>("#nav details")) d.open = next || d.hasAttribute("data-cur");
    setOn(next);
  }
  return (
    <form action={action}>
      <input type="hidden" name="back" value={back} />
      <button name="rail" value={on ? "off" : "on"} title={on ? "Expand the sidebar" : "Collapse the sidebar"} className="side-link w-full font-medium" onClick={flip}>
        {children}<span className="rail:sr-only">{on ? "Expand" : "Collapse"}</span>
      </button>
    </form>
  );
}

export type ThemeChoice = { v: string; label: string; title: string; icon: React.ReactNode };

/** Auto / Light / Dark: the page changes at once; the server action still saves the choice and re-renders in place. */
export function ThemeForm({ cur, csrf, back, compact, themes, action }: { cur: string; csrf: string; back: string; compact: boolean; themes: ThemeChoice[]; action: Action }) {
  const [t, setT] = useState(cur);
  useEffect(() => setT(cur), [cur]);
  const backRef = useRef<HTMLInputElement>(null);
  function pick(v: string) {
    const html = document.documentElement;
    if (v === "light" || v === "dark") html.setAttribute("data-theme", v);
    else html.removeAttribute("data-theme");
    setT(v);
    // The shell is not re-rendered by a client navigation, so its own copy of the URL can be old: send the one shown.
    if (backRef.current) backRef.current.value = location.pathname + location.search;
  }
  return (
    <form action={action} className={compact ? "theme compact" : "theme"} aria-label="Theme">
      <input type="hidden" name="csrf" value={csrf} />
      <input ref={backRef} type="hidden" name="back" defaultValue={back} />
      {themes.map((x) => (
        <button key={x.v} name="theme" value={x.v} aria-pressed={t === x.v} title={x.title} className={t === x.v ? "on" : undefined} onClick={() => pick(x.v)}>
          {x.icon}
          <span className={compact ? "sr" : undefined}>{x.label}</span>
        </button>
      ))}
    </form>
  );
}

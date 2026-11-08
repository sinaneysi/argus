"use client";

import { useRouter } from "next/navigation";
import { useEffect } from "react";
import type { Reload as R } from "@/lib/types";
import { appPath } from "./Soft";

// The one way a page follows live work (DESIGN.md §10). A <meta http-equiv="refresh"> must not be rendered bare: the
// browser schedules it once and a client navigation away does not cancel it, so the old page comes back seconds later.
// With JavaScript a timer re-renders the page in place (router.refresh) and is cleared when the page goes; without
// JavaScript the meta refresh inside <noscript> still works. No inline script, so the strict CSP needs nothing extra.

export type ReloadValue = R | boolean | number | undefined;

/** Seconds and hand-off URL of a refresh value: true is 5 s, a number is seconds, an object can name a URL. */
export function reloadOf(r: ReloadValue): { seconds: number; url?: string } | null {
  if (!r) return null;
  if (r === true) return { seconds: 5 };
  if (typeof r === "number") return r > 0 ? { seconds: r } : null;
  return r.seconds > 0 ? { seconds: r.seconds, url: r.url || undefined } : null;
}

/** The user is writing: a form field has focus or holds an unsent edit. The refresh waits for the next tick. */
export function busy(doc: Document): boolean {
  const a = doc.activeElement;
  if (a instanceof HTMLElement && (a.isContentEditable || a.matches("textarea, select, input:not([type=hidden],[type=submit],[type=button],[type=reset],[type=checkbox],[type=radio])"))) return true;
  for (const f of doc.querySelectorAll<HTMLInputElement | HTMLTextAreaElement>("textarea, input[type=text], input[type=search], input:not([type])")) {
    if (f.value !== f.defaultValue) return true;
  }
  return false;
}

export function Reload({ r }: { r: ReloadValue }) {
  const router = useRouter();
  const v = reloadOf(r);
  const seconds = v?.seconds ?? 0;
  const url = v?.url;
  useEffect(() => {
    if (!seconds) return;
    const here = location.href;
    let timer = 0;
    const arm = () => { timer = window.setTimeout(tick, seconds * 1000); };
    const tick = () => {
      if (document.hidden || busy(document)) return arm();
      if (!url) {
        router.refresh();
        return arm(); // the next render keeps this timer, or stops it when the page no longer asks
      }
      if (location.href !== here) return; // the user went on: never pull them back
      const u = appPath(url, location.href);
      if (u) router.replace(u.pathname + u.search + u.hash);
      else location.replace(url);
    };
    arm();
    return () => window.clearTimeout(timer);
  }, [seconds, url, router]);
  if (!v) return null;
  return <noscript><meta httpEquiv="refresh" content={url ? `${seconds};url=${url}` : String(seconds)} /></noscript>;
}

"use client";

import Form from "next/form";
import Link from "next/link";
import { usePathname, useRouter } from "next/navigation";
import { useEffect } from "react";

// Client navigation (DESIGN.md §10: a click must not reload the page). The shell's sidebar and top bar are parallel route
// slots (app/(app)/@side, @top), so a client navigation re-renders them for the new page like a document load would.
// Without JavaScript, and outside the router (tests), every link and form here is plain HTML.

/** Paths that are not pages of this app: the API's own routes (next.config.ts rewrites), files, and the pages that must
 * load as a document (sign-in, and Two-factor, which the layout renders without the shell). */
const HARD = /^\/(api|auth|static|_next|healthz|login|logout)(\/|$)|^\/account\/2fa(\/|$)|\.[a-z0-9]{1,5}$/i;

/** The in-app path of a URL, or null when it must be a document load. */
export function appPath(href: string, base: string): URL | null {
  let u: URL;
  try {
    u = new URL(href, base);
  } catch {
    return null;
  }
  const origin = new URL(base).origin;
  if (u.origin !== origin || HARD.test(u.pathname)) return null;
  return u;
}

function samePath(href: string, path: string | null): boolean {
  if (!path) return false;
  try {
    return new URL(href, "http://x" + path).pathname === path;
  } catch {
    return false;
  }
}

type GetProps = Omit<React.ComponentProps<"form">, "action" | "method"> & { action: string; scroll?: boolean };

/** A GET form (filters, search): navigates with next/form; on its own page it keeps the scroll unless told. */
export function GetForm({ action, scroll, onSubmit, ...p }: GetProps) {
  const path = usePathname();
  if (!path || !appPath(action, "http://x" + path)) return <form method="get" action={action} onSubmit={onSubmit} {...p} />;
  return (
    <Form
      action={action}
      scroll={scroll ?? !samePath(action, path)}
      prefetch={false}
      onSubmit={(e) => {
        onSubmit?.(e);
        // next/form builds the URL from the form's fields alone; a named submit button (Accept…, Reject…) is a field too.
        const s = (e.nativeEvent as SubmitEvent).submitter;
        if (s instanceof HTMLButtonElement && s.name) {
          const x = document.createElement("input");
          x.type = "hidden";
          x.name = s.name;
          x.value = s.value;
          e.currentTarget.append(x);
          setTimeout(() => x.remove());
        }
      }}
      {...p}
    />
  );
}

type LinkProps = React.ComponentProps<"a"> & { scroll?: boolean };

/** A tab, chip or pager link: a client navigation; on its own page it keeps the scroll. */
export function SoftLink({ href, scroll, ...p }: LinkProps) {
  const path = usePathname();
  if (!href || !path || !appPath(href, "http://x" + path)) return <a href={href} {...p} />;
  return <Link href={href} scroll={scroll ?? !samePath(href, path)} prefetch={false} {...p} />;
}

/** Every plain `<a>` to another page of the app becomes a client navigation: one listener for the whole document, so
 * server-rendered links stay plain HTML. It steps aside for modified clicks, new tabs, downloads, `data-hard`, links
 * that only move to a fragment, and links inside the open phone sheet (it is opened by `:target`, which a history push
 * does not clear). Mounted once in the app shell. */
export function Navigate() {
  const router = useRouter();
  useEffect(() => {
    const onClick = (e: MouseEvent) => {
      if (e.defaultPrevented || e.button !== 0 || e.metaKey || e.ctrlKey || e.shiftKey || e.altKey) return;
      const a = e.target instanceof Element ? e.target.closest("a[href]") : null;
      if (!(a instanceof HTMLAnchorElement)) return;
      if ((a.target && a.target !== "_self") || a.hasAttribute("download") || a.hasAttribute("data-hard")) return;
      const u = appPath(a.getAttribute("href") ?? "", location.href);
      if (!u) return;
      const here = location.pathname + location.search;
      if (u.pathname + u.search === here && u.hash) return; // a jump inside the page: the browser does it
      if (location.hash === "#nav" && a.closest("#nav")) return;
      e.preventDefault();
      router.push(u.pathname + u.search + u.hash, { scroll: u.pathname !== location.pathname });
    };
    window.addEventListener("click", onClick);
    return () => window.removeEventListener("click", onClick);
  }, [router]);
  return null;
}

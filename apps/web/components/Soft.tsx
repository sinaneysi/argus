"use client";

import Form from "next/form";
import Link from "next/link";
import { usePathname } from "next/navigation";

// Client navigation for GET forms and links that stay on the page they are on (DESIGN.md §10: a click must not reload
// the page). Only the same path: the shell (sidebar, top bar) is not re-rendered by a client navigation, so a move to
// another page stays a plain document load. Without JavaScript, and outside the router (tests), both are plain HTML.

function samePath(href: string, path: string | null): boolean {
  if (!path) return false;
  try {
    return new URL(href, "http://x" + path).pathname === path;
  } catch {
    return false;
  }
}

type GetProps = Omit<React.ComponentProps<"form">, "action" | "method"> & { action: string; scroll?: boolean };

/** A GET form (filters, search): on its own page it navigates with next/form and keeps the scroll unless told. */
export function GetForm({ action, scroll = false, onSubmit, ...p }: GetProps) {
  const path = usePathname();
  if (!samePath(action, path)) return <form method="get" action={action} onSubmit={onSubmit} {...p} />;
  return (
    <Form
      action={action}
      scroll={scroll}
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

/** A tab or filter link: on its own page a client navigation that keeps the scroll; elsewhere a plain link. */
export function SoftLink({ href, scroll = false, ...p }: LinkProps) {
  const path = usePathname();
  if (!href || !samePath(href, path)) return <a href={href} {...p} />;
  return <Link href={href} scroll={scroll} prefetch={false} {...p} />;
}

import { HELP_FOR } from "@/lib/help";

/** The small "?" right after a page's title: that page's section in Help. */
export function HelpQ({ p }: { p: string }) {
  const to = HELP_FOR[p];
  return to ? <a className="help-q" href={to} title="Help for this page" aria-label="Help for this page">?</a> : null;
}

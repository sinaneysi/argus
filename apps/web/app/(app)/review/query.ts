// The Review page's URL state, in the same order and with the same defaults as the Go page (views_review.go rvQuery).
import type { Review } from "@/lib/types";

export type Q = Review["query"];

export function href(q: Partial<Q>): string {
  const p = new URLSearchParams();
  const add = (k: string, v: string | number | undefined, skip = false) => {
    if (v !== undefined && v !== "" && !skip) p.set(k, String(v));
  };
  add("kind", q.kind);
  add("q", q.q);
  add("sev", q.sev);
  add("env", q.env);
  add("src", q.src);
  add("sort", q.sort, q.sort === "urgency");
  add("page", q.page, (q.page ?? 1) <= 1);
  add("sel", q.sel);
  add("step", q.step);
  const s = p.toString().replace(/%3A/g, ":");
  return s ? "/review?" + s : "/review";
}

/** The queue this view came from: the same filters and page, nothing selected. */
export const queueOf = (q: Q): Partial<Q> => ({ ...q, sel: "", step: "" });

export const anchorOf = (sel: string) => "rv-" + sel.replace(":", "-").replace(/[^A-Za-z0-9_-]/g, "-");

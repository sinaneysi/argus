import { Icon } from "./Icon";
import type { Banner as B } from "@/lib/types";

const GLYPH = { ok: "check", warn: "warn", fail: "fail", info: "info" } as const;

/** A request or state banner (DESIGN.md 3.19): tint pair, glyph and words; failures are announced as alerts. */
export function Banner({ b }: { b: B }) {
  return (
    <div className={`banner ${b.kind}`} role={b.kind === "fail" ? "alert" : "status"}>
      <Icon name={GLYPH[b.kind]} />
      <span>{b.segs?.length ? b.segs.map((x, i) => (x.href ? <a key={i} href={x.href}>{x.text}</a> : <span key={i}>{x.text}</span>)) : b.text}</span>
    </div>
  );
}

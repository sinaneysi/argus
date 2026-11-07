import { Pill } from "./Pills";
import { tone } from "./Section";
import type { StatusVerdict } from "@/lib/types";
import { buttonVariants } from "@/components/ui/button";
import { PlainCard } from "@/components/ui/card";

/** The answer on top of a Settings page: one state, one sentence, at most one next action, then the facts behind it. */
export function StatusBlock({ id, s }: { id: string; s: StatusVerdict }) {
  return (
    <PlainCard as="section" className={`cl-now ${s.kind}`} id={id} aria-labelledby={`${id}-h`}>
      <div className="cl-now-t"><Pill tone={tone(s.kind)}>{s.badge}</Pill><h2 id={`${id}-h`}>{s.head}</h2></div>
      <p className="set-note">{s.detail}</p>
      {s.act ? <div className="row-act"><a data-slot="button" className={buttonVariants({ variant: "outline" })} href={s.href}>{s.act}</a></div> : null}
      <dl className="cl-facts">{s.facts.map((f) => <div key={f.label} className={f.kind || undefined}><dt>{f.label}</dt><dd>{f.href ? <a href={f.href}>{f.value}</a> : f.value}</dd></div>)}</dl>
    </PlainCard>
  );
}

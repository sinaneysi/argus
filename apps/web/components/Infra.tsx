import "./infra.css";
import { Icon } from "./Icon";
import { Pill } from "./Pills";
import { Hidden, tone, When } from "./Section";
import { backupOverride, scanAct } from "@/app/(app)/inventory/actions";
import { ago, full, iso } from "@/lib/format";
import type { Accepted, MeterData, MiniData, PillData, Reload as R, Restore, RunStrip, ScanBar } from "@/lib/types";
import { Button } from "@/components/ui/button";

// The parts the Infrastructure pages share. Server components; meters and small charts are plain SVG attributes, so
// the strict CSP needs nothing extra.

export function Reload({ r }: { r: R | boolean | undefined }) {
  if (!r) return null;
  if (r === true) return <meta httpEquiv="refresh" content="5" />;
  return <meta httpEquiv="refresh" content={r.url ? `${r.seconds};url=${r.url}` : String(r.seconds)} />;
}

export function P({ p }: { p: PillData }) {
  return <Pill tone={tone(p.kind)} title={p.title}>{p.text}</Pill>;
}

/** The scan control of a page head. */
export function Scan({ s, csrf, small }: { s: ScanBar; csrf: string; small?: boolean }) {
  return (
    <div className="scanctl">
      {s.overdue ? <span className="muted small" role="status">{s.overdue} · <a href="/runs">Runs</a></span>
        : <span className="muted small" role={s.busy ? "status" : undefined}>{s.meta}</span>}
      {s.busy ? <Button variant="outline" disabled title={s.meta}><Icon name="reset" />Scanning…</Button> : (
        <form action={scanAct}><Hidden csrf={csrf} tasks={s.tasks} back={s.back} /><Button variant="outline" size={small ? "sm" : "default"} title={s.title}><Icon name="reset" />{s.label}</Button></form>
      )}
    </div>
  );
}

export function Bar({ pct, kind }: { pct: number; kind: string }) {
  return (
    <svg className="bar" viewBox="0 0 100 8" preserveAspectRatio="none" aria-hidden="true">
      <rect width="100" height="8" className="track" /><rect width={pct.toFixed(1)} height="8" className={kind} />
    </svg>
  );
}

export function Meter({ m }: { m: MeterData }) {
  if (!m) return <span className="muted">-</span>;
  return <span className="meter" title={m.title}><Bar pct={m.pct} kind={m.kind} /><span className="num">{m.figure}</span></span>;
}

export function Mini({ m }: { m: MiniData | null }) {
  if (!m) return <span className="muted">-</span>;
  return (
    <svg className="mini" viewBox="0 0 120 28" preserveAspectRatio="none" role="img" aria-label={m.label}>
      <title>{m.label}</title>
      <path d={m.area} className="area" />
      {m.rule ? <line x1="0" y1={m.rule} x2="120" y2={m.rule} className="thr" vectorEffect="non-scaling-stroke" /> : null}
      <polyline points={m.line} className="line" vectorEffect="non-scaling-stroke" />
    </svg>
  );
}

export function Strip({ s }: { s: RunStrip }) {
  return (
    <>
      <svg className="strip" viewBox="0 0 480 16" preserveAspectRatio="none" role="img" aria-label={s.label}>
        <rect width="480" height="16" className="track" />
        {s.mids.map((x) => <line key={x} x1={x} y1="0" x2={x} y2="16" className="mid" />)}
        {s.marks.map((m, i) => <line key={i} x1={m.x} y1="3" x2={m.x} y2="13" className={`tk ${m.kind}`} vectorEffect="non-scaling-stroke"><title>{m.title}</title></line>)}
      </svg>
      <p className="strip-x"><span>{s.hours} h ago</span><span>{s.label}</span><span>now</span></p>
    </>
  );
}

/** A line of time and size, with its numbers in a fold (DESIGN.md 3.21). */
export function Line({ points, low, high, label }: { points: [number, number][]; low: number; high: number; label: string }) {
  const t0 = points[0]![0];
  const span = points[points.length - 1]![0] - t0 || 1;
  const pad = (high - low) * 0.1 || high * 0.1 || 1;
  const lo = Math.max(0, low - pad);
  const hi = high + pad;
  const xy = points.map(([t, v]) => `${(((t - t0) / span) * 1000).toFixed(1)},${(220 - ((v - lo) / (hi - lo)) * 210).toFixed(1)}`).join(" ");
  return (
    <svg className="line-c" viewBox="0 0 1000 230" preserveAspectRatio="none" role="img" aria-label={label}>
      <title>{label}</title>
      {[10, 80, 150, 220].map((y) => <line key={y} x1="0" y1={y} x2="1000" y2={y} className="grid" vectorEffect="non-scaling-stroke" />)}
      <polyline points={xy} className="line" vectorEffect="non-scaling-stroke" />
    </svg>
  );
}

export function Note({ children }: { children: React.ReactNode }) {
  return <p className="l-note">{children}</p>;
}

/** Who accepted a failing backup set, for how long and why, with Undo for admins. */
export function AcceptedNote({ a, back, csrf }: { a: Accepted; back: string; csrf: string }) {
  return (
    <div className="acc">
      <span className="muted small">Accepted by <span className="mono">{a.by}</span> {a.scope}</span>
      <span className="small" dir="auto">{a.reason}</span>
      {a.canUndo ? <form action={backupOverride}><Hidden csrf={csrf} action="undo" id={a.id} back={back} /><Button variant="ghost" size="sm" title="Count this set as failing again">Undo</Button></form> : null}
    </div>
  );
}

export function RestoreText({ r }: { r: Restore }) {
  if (!r.kind) return <span className="muted">never</span>;
  return <><Pill tone={tone(r.kind)}>{r.result}</Pill> <span className="nw">{r.date}</span></>;
}

export const At = ({ ts }: { ts: number }) => (ts ? <time dateTime={iso(ts)} title={full(ts)}>{ago(ts)}</time> : <span className="muted">never</span>);
export { When };

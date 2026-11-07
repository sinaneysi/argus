import "./agent.css";
import { Inline } from "./Markdown";
import type { CostDay } from "@/lib/types";
import { PlainCard } from "@/components/ui/card";

const W = 300; // viewBox width: ten units a day
const H = 100;

/** Cost per day as bars (DESIGN.md 3.21), with the numbers in a fold. The axis words are HTML, so they stay readable on a phone. */
export function CostChart({ days, top, topLabel }: { days: CostDay[]; top: number; topLabel: string }) {
  const step = W / Math.max(days.length, 1);
  const spent = days.filter((d) => d.usd > 0);
  if (!spent.length) return <p className="empty pad">No cost in the last 30 days.</p>;
  return (
    <div className="cost">
      <div className="cost-plot">
        <svg viewBox={`0 0 ${W} ${H}`} preserveAspectRatio="none" role="img" aria-label={`Agent cost per day, last 30 days; highest ${topLabel}`}>
          {[0, 25, 50, 75].map((y) => <line key={y} x1="0" x2={W} y1={y} y2={y} className="grid" vectorEffect="non-scaling-stroke" />)}
          {days.map((d, i) => {
            const h = top > 0 ? (d.usd / top) * (H - 2) : 0;
            return h > 0 ? <rect key={d.day} className="bar-d" x={i * step + step * 0.15} y={H - h} width={step * 0.7} height={h}><title>{`${d.day}: ${d.label}`}</title></rect> : null;
          })}
          <line x1="0" x2={W} y1={H} y2={H} className="base" vectorEffect="non-scaling-stroke" />
        </svg>
        <span className="cost-top small muted num">{topLabel}</span>
      </div>
      <div className="cost-x small muted num"><span>{days[0]?.day}</span><span>{days[days.length - 1]?.day}</span></div>
      <details className="fold"><summary>Show data</summary>
        <div className="tablewrap" role="region" aria-label="Cost per day" tabIndex={0}><table className="l-table"><thead><tr><th scope="col">Day</th><th scope="col" className="num">Cost</th></tr></thead>
          <tbody>{spent.map((d) => <tr key={d.day}><td data-label="Day" className="nw">{d.day}</td><td data-label="Cost" className="num">{d.label}</td></tr>)}</tbody></table></div>
      </details>
    </div>
  );
}

/** What the operator does to set the fixer up; Argus never grants itself access. */
export function OperatorSteps({ steps }: { steps: string[] }) {
  if (!steps.length) return null;
  return (
    <PlainCard as="section" className="l-card" aria-labelledby="op-h"><div className="card-h pad"><h2 id="op-h">Operator steps</h2></div>
      <ol className="op-steps">{steps.map((s, i) => <li key={i}><Inline text={s} codeOnly /></li>)}</ol></PlainCard>
  );
}

export function AdminsOnly() {
  return <PlainCard as="section"><p className="empty"><b>Admins only.</b> This page needs the admin role.</p></PlainCard>;
}

export function CellTable({ title, id, head, rows }: { title: string; id: string; head: string[]; rows: string[][] }) {
  return (
    <PlainCard as="section" className="l-card" aria-labelledby={id}><div className="card-h pad"><h2 id={id}>{title}</h2></div>
      {rows.length ? (
        <div className="tablewrap" role="region" aria-label={title} tabIndex={0}><table className="l-table"><thead><tr>{head.map((h) => <th key={h} scope="col">{h}</th>)}</tr></thead>
          <tbody>{rows.map((r, i) => <tr key={i}>{r.map((c, j) => <td key={j} data-label={head[j]} className={head[j] === "Command" || head[j] === "Path" ? "mono" : undefined}>{c}</td>)}</tr>)}</tbody></table></div>
      ) : <p className="empty pad">None reported.</p>}
    </PlainCard>
  );
}

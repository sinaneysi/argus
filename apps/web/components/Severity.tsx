const SEV: Record<string, { word: string; bars: number }> = {
  critical: { word: "Critical", bars: 4 },
  high: { word: "High", bars: 3 },
  medium: { word: "Medium", bars: 2 },
  low: { word: "Low", bars: 1 },
  none: { word: "No severity", bars: 0 },
};

/** Signal bars: more bars, more severe. The count carries the level, so it reads without colour. */
function Bars({ bars, size }: { bars: number; size: number }) {
  return (
    <svg width={size} height={size} viewBox="0 0 24 24" fill="currentColor" aria-hidden="true">
      {[6, 11, 16, 21].map((h, i) => <rect key={h} x={1 + i * 6} y={22 - h} width={4} height={h} rx={1} opacity={i < bars ? 1 : 0.2} />)}
    </svg>
  );
}

/** The severity mark of a queue row, with the word for screen readers and on hover. */
export function SevMark({ sev }: { sev: string }) {
  const s = SEV[sev] ?? SEV.none!;
  return <span className={`sev-mark sev-${sev}`} title={s.word}><Bars bars={s.bars} size={18} /><span className="sr">{s.word}</span></span>;
}

/** The severity pill: bars and the word. */
export function SevPill({ sev, word }: { sev: string; word?: string }) {
  const s = SEV[sev] ?? SEV.none!;
  return <span className={`sev-pill sev-${sev}`}><Bars bars={s.bars} size={14} />{word ? word.charAt(0).toUpperCase() + word.slice(1) : sev === "none" ? "unknown" : s.word}</span>;
}

// Icons: Lucide (ISC licence, https://lucide.dev), outline 1.5px, inlined so the CSP needs nothing extra.

type Shape = { d?: string; c?: [number, number, number]; r?: [number, number, number, number, number] };

const P = (d: string): Shape => ({ d });
const C = (cx: number, cy: number, r: number): Shape => ({ c: [cx, cy, r] });
const R = (x: number, y: number, w: number, h: number, rx: number): Shape => ({ r: [x, y, w, h, rx] });

const ICONS: Record<string, Shape[]> = {
  sun: [C(12, 12, 4), P("M12 2v2"), P("M12 20v2"), P("m4.93 4.93 1.41 1.41"), P("m17.66 17.66 1.41 1.41"), P("M2 12h2"),
    P("M20 12h2"), P("m6.34 17.66-1.41 1.41"), P("m19.07 4.93-1.41 1.41")],
  moon: [P("M12 3a6 6 0 0 0 9 9 9 9 0 1 1-9-9Z")],
  monitor: [R(2, 3, 20, 14, 2), P("M8 21h8"), P("M12 17v4")],
  search: [C(11, 11, 8), P("m21 21-4.3-4.3")],
  menu: [P("M4 6h16"), P("M4 12h16"), P("M4 18h16")],
  x: [P("M18 6 6 18"), P("m6 6 12 12")],
  chevron: [P("m9 18 6-6-6-6")],
  panel: [R(3, 3, 18, 18, 2), P("M9 3v18")],
  arrow: [P("M5 12h14"), P("m12 5 7 7-7 7")],
  layers: [P("M12.83 2.18a2 2 0 0 0-1.66 0L2.6 6.08a1 1 0 0 0 0 1.83l8.58 3.91a2 2 0 0 0 1.66 0l8.58-3.9a1 1 0 0 0 0-1.83z"), P("M2 12a1 1 0 0 0 .58.91l8.6 3.91a2 2 0 0 0 1.65 0l8.58-3.9A1 1 0 0 0 22 12"), P("M2 17a1 1 0 0 0 .58.91l8.6 3.91a2 2 0 0 0 1.65 0l8.58-3.9A1 1 0 0 0 22 17")],
  bot: [P("M12 8V4H8"), R(4, 8, 16, 12, 2), P("M2 14h2"), P("M20 14h2"), P("M15 13v2"), P("M9 13v2")],
  box: [P("M21 8a2 2 0 0 0-1-1.73l-7-4a2 2 0 0 0-2 0l-7 4A2 2 0 0 0 3 8v8a2 2 0 0 0 1 1.73l7 4a2 2 0 0 0 2 0l7-4A2 2 0 0 0 21 16Z"), P("m3.3 7 8.7 5 8.7-5"), P("M12 22V12")],
  workflow: [R(3, 3, 8, 8, 2), P("M7 11v4a2 2 0 0 0 2 2h4"), R(13, 13, 8, 8, 2)],
  bell: [P("M10.268 21a2 2 0 0 0 3.464 0"), P("M3.262 15.326A1 1 0 0 0 4 17h16a1 1 0 0 0 .74-1.673C19.41 13.956 18 12.499 18 8A6 6 0 0 0 6 8c0 4.499-1.411 5.956-2.738 7.326")],
  target: [C(12, 12, 10), C(12, 12, 6), C(12, 12, 2)],
  book: [P("M4 19.5v-15A2.5 2.5 0 0 1 6.5 2H20v20H6.5a2.5 2.5 0 0 1 0-5H20")],
  gauge: [P("m12 14 4-4"), P("M3.34 19a10 10 0 1 1 17.32 0")],
  lock: [R(3, 11, 18, 11, 2), P("M7 11V7a5 5 0 0 1 10 0v4")],
  database: [P("M3 5a9 3 0 1 0 18 0a9 3 0 1 0-18 0"), P("M3 5v14a9 3 0 0 0 18 0V5"), P("M3 12a9 3 0 0 0 18 0")],
  "shield-check": [P("M20 13c0 5-3.5 7.5-7.66 8.95a1 1 0 0 1-.67-.01C7.5 20.5 4 18 4 13V6a1 1 0 0 1 1-1c2 0 4.5-1.2 6.24-2.72a1.17 1.17 0 0 1 1.52 0C14.51 3.81 17 5 19 5a1 1 0 0 1 1 1z"), P("m9 12 2 2 4-4")],
  "shield-alert": [P("M20 13c0 5-3.5 7.5-7.66 8.95a1 1 0 0 1-.67-.01C7.5 20.5 4 18 4 13V6a1 1 0 0 1 1-1c2 0 4.5-1.2 6.24-2.72a1.17 1.17 0 0 1 1.52 0C14.51 3.81 17 5 19 5a1 1 0 0 1 1 1z"), P("M12 8v4"), P("M12 16h.01")],
  filter: [P("M22 3H2l8 9.46V19l4 2v-8.54L22 3z")],
  kanban: [R(3, 3, 18, 18, 2), P("M8 7v7"), P("M12 7v4"), P("M16 7v9")],
  message: [P("M21 15a2 2 0 0 1-2 2H7l-4 4V5a2 2 0 0 1 2-2h14a2 2 0 0 1 2 2z")],
  plus: [P("M5 12h14"), P("M12 5v14")],
  bulb: [P("M15 14c.2-1 .7-1.7 1.5-2.5 1-.9 1.5-2.2 1.5-3.5A6 6 0 0 0 6 8c0 1 .2 2.2 1.5 3.5.7.7 1.3 1.5 1.5 2.5"), P("M9 18h6"), P("M10 22h4")],
  wrench: [P("M14.7 6.3a1 1 0 0 0 0 1.4l1.6 1.6a1 1 0 0 0 1.4 0l3.77-3.77a6 6 0 0 1-7.94 7.94l-6.91 6.91a2.12 2.12 0 0 1-3-3l6.91-6.91a6 6 0 0 1 7.94-7.94l-3.76 3.76z")],
  back: [P("M19 12H5"), P("m12 19-7-7 7-7")],
  logout: [P("M9 21H5a2 2 0 0 1-2-2V5a2 2 0 0 1 2-2h4"), P("m16 17 5-5-5-5"), P("M21 12H9")],
  megaphone: [P("M11 6a13 13 0 0 0 8.4-2.8A1 1 0 0 1 21 4v12a1 1 0 0 1-1.6.8A13 13 0 0 0 11 14H5a2 2 0 0 1-2-2V8a2 2 0 0 1 2-2z"), P("M6 14a12 12 0 0 0 2.4 7.2 2 2 0 0 0 3.2-2.4A8 8 0 0 1 10 14"), P("M8 6v8")],
  trending: [P("M22 7 13.5 15.5 8.5 10.5 2 17"), P("M16 7h6v6")],
  help: [C(12, 12, 10), P("M9.09 9a3 3 0 0 1 5.83 1c0 2-3 3-3 3"), P("M12 17h.01")],
  settings: [P("M20 7h-9"), P("M14 17H5"), C(17, 17, 3), C(7, 7, 3)],
  check: [P("M20 6 9 17l-5-5")],
  warn: [P("m21.73 18-8-14a2 2 0 0 0-3.48 0l-8 14A2 2 0 0 0 4 21h16a2 2 0 0 0 1.73-3"), P("M12 9v4"), P("M12 17h.01")],
  fail: [C(12, 12, 10), P("m15 9-6 6"), P("m9 9 6 6")],
  info: [C(12, 12, 10), P("M12 16v-4"), P("M12 8h.01")],
  reset: [P("M3 12a9 9 0 1 0 9-9 9.75 9.75 0 0 0-6.74 2.74L3 8"), P("M3 3v5h5")],
  clock: [C(12, 12, 10), P("M12 6v6l4 2")],
  home: [P("M15 21v-8a1 1 0 0 0-1-1h-4a1 1 0 0 0-1 1v8"),
    P("M3 10a2 2 0 0 1 .709-1.528l7-6a2 2 0 0 1 2.582 0l7 6A2 2 0 0 1 21 10v9a2 2 0 0 1-2 2H5a2 2 0 0 1-2-2z")],
  inbox: [P("M22 12h-6l-2 3h-4l-2-3H2"), P("M5.45 5.11 2 12v6a2 2 0 0 0 2 2h16a2 2 0 0 0 2-2v-6l-3.45-6.89A2 2 0 0 0 16.76 4H7.24a2 2 0 0 0-1.79 1.11z")],
  activity: [P("M22 12h-4l-3 9L9 3l-3 9H2")],
  server: [R(2, 2, 20, 8, 2), R(2, 14, 20, 8, 2), P("M6 6h.01"), P("M6 18h.01")],
  branch: [P("M6 3v12"), C(18, 6, 3), C(6, 18, 3), P("M18 9a9 9 0 0 1-9 9")],
  list: [P("m3 17 2 2 4-4"), P("m3 7 2 2 4-4"), P("M13 6h8"), P("M13 12h8"), P("M13 18h8")],
  terminal: [P("m4 17 6-6-6-6"), P("M12 19h8")],
  dot: [C(12, 12, 1)],
};

export type IconName = keyof typeof ICONS;

export function Icon({ name, size = 16, className }: { name: string; size?: number; className?: string }) {
  const shapes = ICONS[name] ?? ICONS.dot!;
  return (
    <svg className={className ? `icon ${className}` : "icon"} width={size} height={size} viewBox="0 0 24 24" fill="none"
      stroke="currentColor" strokeWidth={1.5} strokeLinecap="round" strokeLinejoin="round" aria-hidden="true" focusable="false">
      {shapes.map((s, i) =>
        s.d ? <path key={i} d={s.d} /> : s.c ? <circle key={i} cx={s.c[0]} cy={s.c[1]} r={s.c[2]} /> :
          s.r ? <rect key={i} x={s.r[0]} y={s.r[1]} width={s.r[2]} height={s.r[3]} rx={s.r[4]} /> : null)}
    </svg>
  );
}

/** The Argus mark: the eye whose upper lid peaks like an A, on its tile (colours from tokens: --mark-bg, --mark-fg). */
export function Mark({ size = 28 }: { size?: number }) {
  return (
    <svg className="mark" width={size} height={size} viewBox="0 0 32 32" aria-hidden="true" focusable="false">
      <rect className="mark-bg" width="32" height="32" rx="8" />
      <path className="mark-lid" d="M6 18 16 8l10 10M6 18q10 9 20 0" fill="none" strokeWidth="2.6" strokeLinecap="round" strokeLinejoin="round" />
      <circle className="mark-iris" cx="16" cy="17.5" r="3.2" />
    </svg>
  );
}

/** The eye alone (the "Needs you" glyph), in the accent. */
export function Eye({ size = 16 }: { size?: number }) {
  return (
    <svg className="icon eye" width={size} height={size} viewBox="4 6 24 22" fill="none" stroke="currentColor" strokeWidth={2.2}
      strokeLinecap="round" strokeLinejoin="round" aria-hidden="true" focusable="false">
      <path d="M6 18 16 8l10 10M6 18q10 9 20 0" />
      <circle cx="16" cy="17.5" r="2.6" fill="currentColor" stroke="none" />
    </svg>
  );
}

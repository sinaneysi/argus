// Formatting shared by the pages: durations, clock times and ages, in the server's time zone (TZ).

export function minutes(m: number): string {
  if (m < 60) return `${m} min`;
  const h = Math.floor(m / 60);
  const r = m % 60;
  return r === 0 ? `${h} h` : `${h} h ${r} min`;
}

export function hours(m: number): string {
  return m % 60 === 0 ? `${m / 60} h` : `${(m / 60).toFixed(1)} h`;
}

const clockFmt = new Intl.DateTimeFormat("en-GB", { hour: "2-digit", minute: "2-digit", hourCycle: "h23" });
const dayFmt = new Intl.DateTimeFormat("en-GB", { day: "numeric", month: "short" });
// sv-SE writes ISO order, 2026-11-06 04:32, the format Go's When() sends: one date format on every page.
const fullFmt = new Intl.DateTimeFormat("sv-SE", {
  year: "numeric", month: "2-digit", day: "2-digit", hour: "2-digit", minute: "2-digit", hourCycle: "h23",
});

export const clock = (ts: number) => clockFmt.format(new Date(ts * 1000));
export const day = (ts: number) => dayFmt.format(new Date(ts * 1000));
export const full = (ts: number) => fullFmt.format(new Date(ts * 1000)).replace(",", "");
export const iso = (ts: number) => new Date(ts * 1000).toISOString();

export function ago(ts: number, now: number = Date.now() / 1000): string {
  const s = Math.floor(now - ts);
  if (ts <= 0) return "never";
  if (s >= 86400) return `${Math.floor(s / 86400)}d ago`;
  if (s >= 3600) return `${Math.floor(s / 3600)}h ago`;
  if (s >= 60) return `${Math.floor(s / 60)}m ago`;
  return "just now";
}

export function plural(n: number, one: string, many = one + "s"): string {
  return `${n} ${n === 1 ? one : many}`;
}

/** A local path to go to; anything else (another host, //, \, controls) is the fallback. */
export function safeNext(n: string | null | undefined, fallback = "/today"): string {
  if (!n || !n.startsWith("/") || n.startsWith("//") || n.includes("\\") || n.startsWith("/auth/")) return fallback;
  return /^[\x21-\x7e]+$/.test(n) ? n : fallback;
}

/** Where a write refused for two-factor goes: the step-up page (or enrolment), which comes back to the page after the code. */
export function stepUpPath(data: unknown, back: string): string | null {
  const d = (data ?? {}) as { mfa?: unknown; sensitive?: unknown };
  const next = encodeURIComponent(safeNext(back, "/today"));
  if (d.mfa === "enrol") return `/account/2fa?next=${next}`;
  if (d.mfa === "stepup") return `/account/confirm?next=${next}${d.sensitive ? "&sensitive=1" : ""}`;
  return null;
}

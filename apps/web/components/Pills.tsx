type Tone = "ok" | "warn" | "fail" | "info" | "neutral";

/** A status or impact pill (DESIGN.md 3.8, 3.14): a word, the tint second. */
export function Pill({ tone = "neutral", title, children }: { tone?: Tone; title?: string; children: React.ReactNode }) {
  return <span className={`pill ${tone}`} title={title}>{children}</span>;
}

/** The environment: production is named in ink with a dot, never alarmed red. */
export function EnvPill({ env, short, label }: { env: string; short?: string; label?: string }) {
  const prod = env === "release" || short === "prod";
  return (
    <span className={prod ? "pill env prod" : "pill env"} title={label ? `${label} (${env})` : env}>
      {prod ? <span className="dot" aria-hidden="true" /> : null}
      {prod ? "prod" : short || label || env}
    </span>
  );
}

export function KindTag({ children }: { children: React.ReactNode }) {
  return <span className="kind">{children}</span>;
}

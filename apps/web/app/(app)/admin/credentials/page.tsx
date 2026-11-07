import type { Metadata } from "next";
import { AdminsOnly } from "@/components/Agent";
import { Scan } from "@/components/Infra";
import { Pill } from "@/components/Pills";
import { Banners, Refresh, tone, When } from "@/components/Section";
import { SettingsTabs } from "@/components/Settings";
import { plural } from "@/lib/format";
import { apiGet, csrfToken, readFlash } from "@/lib/server";
import type { AdminCreds, CredExpires, CredRow } from "@/lib/types";
import { PlainCard } from "@/components/ui/card";

export const metadata: Metadata = { title: "Credentials" };

const PAGE = "/admin/credentials";
const dash = <span className="muted">-</span>;

function Expires({ x }: { x: CredExpires }) {
  if (!x.pill.text) return dash;
  return (
    <>{x.date ? <>{x.date} </> : null}<Pill tone={tone(x.pill.kind)}>{x.pill.text}</Pill>
      {x.rotate ? <div className="small">{x.rotate_now ? <><Pill tone="warn">rotate now</Pill> </> : null}<span className={x.rotate_now ? undefined : "muted"}>{x.rotate}</span></div> : null}
      {x.note ? <div className="muted small">{x.note}</div> : null}</>
  );
}

function Table({ rows, label }: { rows: CredRow[]; label: string }) {
  return (
    <div className="tablewrap" role="region" aria-label={label} tabIndex={0}><table className="l-table">
      <thead><tr><th scope="col">Credential</th><th scope="col">Kind</th><th scope="col">Used by</th><th scope="col">Expires</th><th scope="col">Last used</th><th scope="col">Checked</th></tr></thead>
      <tbody>{rows.map((c) => (
        <tr key={c.id} id={`cred-${c.id}`}>
          <td className="t-main"><b>{c.name}</b>{c.second ? <div className="muted small">{c.second}</div> : null}{c.detail ? <div className="small">{c.detail}</div> : null}</td>
          <td data-label="Kind">{c.kind}</td>
          <td data-label="Used by">{c.used_by}{c.where ? <div className="muted small">{c.where}</div> : null}</td>
          <td className="nw" data-label="Expires"><Expires x={c.expires} /></td>
          <td className="nw" data-label="Last used">{c.last_used ? <When ts={c.last_used} /> : dash}</td>
          <td className="nw" data-label="Checked">{c.checked ? <When ts={c.checked} /> : dash}</td>
        </tr>
      ))}</tbody>
    </table></div>
  );
}

/** Settings → Credentials: the expiry of every token and key Argus uses. Metadata only: no value ever reaches this page. */
export default async function CredentialsPage() {
  const [r, csrf, flash] = await Promise.all([apiGet<AdminCreds>("/api/v1/admin/credentials"), csrfToken(), readFlash()]);
  if (r.status === 403) return <><header className="phead"><div><h1>Credentials</h1></div></header><AdminsOnly /></>;
  if (r.status !== 200) throw new Error(`/api/v1/admin/credentials answered ${r.status}`);
  const d = r.data;
  const th = d.thresholds;
  const sub = !d.checked ? "Expiry of every token and key Argus uses"
    : (d.bad.length ? plural(d.bad.length, "needs attention", "need attention") : `All ${d.bad.length + d.ok.length} credentials OK`) + (d.next ? ` · next expiry: ${d.next.name} in ${d.next.days} d` : "");
  return (
    <>
      <Refresh on={!!d.refresh} />
      <header className="phead"><div><h1>Credentials</h1><p className="psub">{sub}</p></div><div className="phead-act"><Scan s={d.scan} csrf={csrf} /></div></header>
      <SettingsTabs cur={PAGE} />
      <div className="stack">
        <Banners flash={flash} />
        {!d.checked ? <PlainCard as="section"><p className="empty">No credential check yet. It runs daily at 07:20 (argus-cred-expiry.timer); Check now starts it.</p></PlainCard> : <>
          {th ? <p className="set-note">Values are never read by this app or shown: each token checks itself as root on {d.host} and only its metadata (expiry, created, last used) reaches this page. Warn at {th.warn} days (self-check, digest, a Proposed task “Rotate …”), Today item at {th.today} days, incident and Matrix notice at {th.crit} days or when expired or rejected. Without an expiry date: rotate every {th.rotate} days. TLS certificates are checked by cert-check: <a href={d.certs ?? "/certs"}>Certificates</a>.</p> : null}
          {d.bad.length ? <PlainCard as="section" className="l-card" aria-labelledby="cred-bad-h"><div className="card-h rows-h"><h2 id="cred-bad-h">Need attention</h2></div><Table rows={d.bad} label="Credentials that need attention" /></PlainCard> : null}
          {d.ok.length ? (d.bad.length
            ? <PlainCard as="details" className="fold-t"><summary>Credentials OK <span className="ncount">{d.ok.length}</span></summary><Table rows={d.ok} label="Credentials OK" /></PlainCard>
            : <PlainCard as="section" className="l-card" aria-labelledby="cred-ok-h"><div className="card-h rows-h"><h2 id="cred-ok-h">Credentials</h2></div><Table rows={d.ok} label="Credentials" /></PlainCard>) : null}
          {d.absent.length ? <PlainCard as="details" className="fold-t"><summary>Not configured <span className="ncount">{d.absent.length}</span> <span className="muted small">optional, nothing to do</span></summary><Table rows={d.absent} label="Credentials not configured" /></PlainCard> : null}
          {d.howto.length ? <PlainCard as="section" aria-labelledby="cred-how-h"><div className="card-h"><h2 id="cred-how-h">How to rotate</h2></div>
            <p className="set-note">Exact steps per kind. Never paste a new value into a chat, a ticket or this app: it goes straight into its file on {d.host} as root, then Check now.</p>
            {d.howto.map((g) => <details key={g.kind} className="fold-t"><summary>{g.kind} <span className="muted small">{g.note}</span></summary>
              {g.creds.map((c) => <div key={c.name}><h3>{c.name}</h3><ol className="set-steps">{c.steps.map((s, i) => <li key={i}>{s}</li>)}</ol></div>)}</details>)}
          </PlainCard> : null}
        </>}
      </div>
    </>
  );
}

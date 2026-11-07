import type { Metadata } from "next";
import { AdminsOnly } from "@/components/Agent";
import { Pill } from "@/components/Pills";
import { Banners, Crumb, Hidden, tone, When } from "@/components/Section";
import { SettingsTabs } from "@/components/Settings";
import { apiGet, csrfToken, readFlash } from "@/lib/server";
import { plural } from "@/lib/format";
import type { CatMetrics, CatReview, CatSummary, Me } from "@/lib/types";
import { catalogueAct } from "./actions";
import { Button } from "@/components/ui/button";
import { PlainCard } from "@/components/ui/card";

export const metadata: Metadata = { title: "Data sources catalogue" };

const VERDICT: Record<string, string> = { ok: "ok", none: "info", warn: "warn", review: "info" };

function Decision({ it, csrf, admin }: { it: CatReview["items"][number]; csrf: string; admin: boolean }) {
  return (
    <li className="src-row" id={`route-${it.id}`}>
      <div className="src-main">
        <div className="src-t"><b>{it.service}</b><Pill>{it.type}</Pill><Pill tone={it.why === "learned" ? "info" : "neutral"}>{it.why}</Pill><span className="muted small">{Math.round(it.confidence * 100)}%</span></div>
        <div className="mono small">{it.locator}</div>
        <div className="muted small">{it.source} · {it.reason}</div>
      </div>
      {admin ? <div className="u-acts">
        <form action={catalogueAct}><Hidden csrf={csrf} action="confirm" id={String(it.id)} /><Button variant="outline" size="sm" aria-label={`Confirm ${it.service} ${it.locator}`}>Confirm</Button></form>
        <details className="fold"><summary aria-label={`Reject ${it.service} ${it.locator}`}>Reject…</summary><form action={catalogueAct} className="u-confirm"><Hidden csrf={csrf} action="reject" id={String(it.id)} />
          <label className="field"><span>Reason (optional)</span><input name="reason" maxLength={300} /></label><Button variant="destructive" size="sm" aria-label={`Reject this route: ${it.service} ${it.locator}`}>Reject this route</Button></form></details>
        <details className="fold"><summary aria-label={`Edit ${it.service} ${it.locator}`}>Edit…</summary><form action={catalogueAct} className="u-confirm"><Hidden csrf={csrf} action="edit" id={String(it.id)} />
          <label className="field"><span>Locator</span><input name="locator" defaultValue={it.locator} required maxLength={250} /></label>
          <label className="field"><span>Note (optional)</span><input name="note" maxLength={300} /></label><Button variant="outline" size="sm" aria-label={`Save and confirm ${it.service} ${it.locator}`}>Save and confirm</Button></form></details>
      </div> : null}
    </li>
  );
}

/** Settings → Data sources → Catalogue: which service maps to which place in each source, and the routes that wait for a person. */
export default async function CataloguePage() {
  const [s, rv, m, me, csrf, flash] = await Promise.all([apiGet<CatSummary>("/api/v1/sources/catalogue"), apiGet<CatReview>("/api/v1/sources/review"), apiGet<CatMetrics>("/api/v1/sources/metrics"), apiGet<Me>("/api/v1/me"), csrfToken(), readFlash()]);
  if (s.status === 403) return <><header className="phead"><div><Crumb href="/admin/sources" label="Data sources" /><h1>Catalogue</h1></div></header><AdminsOnly /></>;
  if (s.status !== 200 || rv.status !== 200 || m.status !== 200) throw new Error(`/api/v1/sources answered ${s.status} ${rv.status} ${m.status}`);
  const d = s.data;
  const admin = me.data.role === "admin";
  const c = d.counts;
  return (
    <>
      <header className="phead"><div><Crumb href="/admin/sources" label="Data sources" /><h1>Catalogue</h1><p className="psub">Where each service lives in each data source. {d.last_discovery ? <>Last discovery <When ts={d.last_discovery} />.</> : null}</p></div></header>
      <SettingsTabs cur="/admin/sources" />
      <div className="stack">
        <Banners list={[{ text: d.verdict.head, kind: (VERDICT[d.verdict.kind] ?? "info") as "ok" }]} flash={flash} />
        <p className="set-note">{plural(c.services, "service")} · {c.confirmed} confirmed · {c.proposed} waiting · {c.stale} stale · {c.rejected} rejected · {c.unmapped} unmapped</p>
        <PlainCard as="section" id="decisions" aria-labelledby="dec-h"><div className="card-h"><h2 id="dec-h">Route decisions <span className="ncount">{rv.data.total}</span></h2></div>
          <p className="set-note">Routes an agent learned, and discovered routes at {Math.round(rv.data.min_confidence * 100)}% confidence or more, at most {rv.data.cap} at a time.{rv.data.capped ? ` ${plural(rv.data.capped, "more proposal")} wait in the catalogue.` : ""}</p>
          {rv.data.items.length ? <ul className="src-list">{rv.data.items.map((it) => <Decision key={it.id} it={it} csrf={csrf} admin={admin} />)}</ul> : <p className="empty">No route waits for a decision.</p>}
        </PlainCard>
        <PlainCard as="section" className="l-card" aria-labelledby="src-h"><div className="card-h pad"><h2 id="src-h">Sources</h2></div>
          {d.sources.length ? <div className="tablewrap" role="region" aria-label="Sources" tabIndex={0}><table className="l-table">
            <thead><tr><th scope="col">Source</th><th scope="col">Last discovery</th><th scope="col">Found</th><th scope="col"><span className="sr">Actions</span></th></tr></thead>
            <tbody>{d.sources.map((r) => (
              <tr key={r.source}>
                <td className="t-main"><b>{r.source}</b>{r.note ? <div className="muted small">{r.note}</div> : null}</td>
                <td data-label="Last discovery"><Pill tone={tone(r.status === "error" ? "fail" : r.status === "partial" ? "warn" : "ok")}>{r.status}</Pill> <When ts={r.finished_at} /></td>
                <td data-label="Found" className="small">{Object.entries(r.counts ?? {}).map(([k, v]) => `${v} ${k}`).join(", ") || "-"}</td>
                <td>{admin ? <form action={catalogueAct}><Hidden csrf={csrf} action="discover" source={r.source} /><Button variant="outline" size="sm" aria-label={`Discover ${r.source} now`}>Discover now</Button></form> : null}</td>
              </tr>
            ))}</tbody>
          </table></div> : <p className="empty">No discovery has run yet.</p>}
        </PlainCard>
        <PlainCard as="section" className="l-card" aria-labelledby="met-h"><div className="card-h pad"><h2 id="met-h">Routes by type</h2></div>
          {m.data.routes.length ? <div className="tablewrap" role="region" aria-label="Routes by type" tabIndex={0}><table className="l-table">
            <thead><tr><th scope="col">Type</th><th scope="col" className="num">Confirmed</th><th scope="col" className="num">Waiting</th><th scope="col" className="num">Rejected</th><th scope="col" className="num">Stale</th></tr></thead>
            <tbody>{m.data.routes.map((t) => <tr key={t.type}><td className="t-main">{t.type}</td><td className="num" data-label="Confirmed">{t.confirmed}</td><td className="num" data-label="Waiting">{t.proposed}</td><td className="num" data-label="Rejected">{t.rejected}</td><td className="num" data-label="Stale">{t.stale}</td></tr>)}</tbody>
          </table></div> : <p className="empty">No routes yet.</p>}
          {m.data.unmapped.top.length ? <div className="pad"><h3>Unmapped <span className="ncount">{m.data.unmapped.total}</span></h3><ul className="set-list">{m.data.unmapped.top.map((u) => <li key={`${u.source}/${u.kind}/${u.key}`}><span className="mono">{u.source}</span> {u.kind} {u.label || u.key}{u.note ? <span className="muted"> · {u.note}</span> : null}</li>)}</ul></div> : null}
        </PlainCard>
      </div>
    </>
  );
}

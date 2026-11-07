import { HelpQ } from "@/components/HelpQ";
import type { Metadata } from "next";
import { backupOverride } from "../../inventory/actions";
import { AcceptedNote, Line, P } from "@/components/Infra";
import { Banners, Crumb, Hidden, one, type Search, When } from "@/components/Section";
import { qs } from "@/lib/api";
import { full } from "@/lib/format";
import { apiGet, csrfToken, readFlash } from "@/lib/server";
import type { BackupSet } from "@/lib/types";
import { Button } from "@/components/ui/button";
import { PlainCard } from "@/components/ui/card";

export const metadata: Metadata = { title: "Backup set" };

export default async function BackupSetPage({ searchParams }: { searchParams: Search }) {
  const sp = await searchParams;
  const name = one(sp.name);
  const [r, csrf, flash] = await Promise.all([apiGet<BackupSet>("/api/v1/backups/set" + qs({ name, sent: one(sp.sent) })), csrfToken(), readFlash()]);
  if (r.status !== 200) throw new Error(`/api/v1/backups/set answered ${r.status}`);
  const d = r.data;
  const back = `/backups/set${qs({ name })}`;
  const o = d.override;
  return (
    <>
      <header className="phead"><div><Crumb href="/backups" label="Backups" /><h1>{d.name}</h1><HelpQ p="/backups" />
        {!d.empty && d.status ? <p className="meta-line"><P p={d.status} />{d.real ? <span>still <span className="mono">{d.real}</span></span> : null}<span>{d.checks} checks in 30 days</span><span>latest {d.latest}</span></p> : null}</div></header>
      <Banners list={d.banners} flash={flash} />
      {d.empty ? <p className="empty"><b>No history.</b> No check recorded this set in the last 30 days.</p> : (
        <div className="stack">
          {d.accepted ? <PlainCard as="section"><AcceptedNote a={d.accepted} back={back} csrf={csrf} /></PlainCard> : null}
          {o ? (
            <PlainCard as="section" id="accept" aria-labelledby="ov-h"><div className="card-h"><h2 id="ov-h">Mark as OK</h2></div>
              <p className="card-sub">Checked this set and it is fine for now (a planned move, a size that is right)? Argus then leaves it out of failures, Today, the brief and the digest. The page keeps showing the real status, <span className="mono">{o.check}</span>, and a different failure shows again at once. Needs a recent two-factor code; audited.</p>
              <form action={backupOverride} className="ctl-f"><Hidden csrf={csrf} action="add" set={d.name} check={o.check} back={back} />
                <label className="field"><span>Why is it OK?</span><textarea className="ta" name="reason" minLength={10} maxLength={500} rows={3} required placeholder="The job moved to the new cluster; the old one is retired." /><small>At least 10 characters. Shown next to the set for everyone.</small></label>
                <fieldset className="picks"><legend>For how long</legend>
                  <label className="opt"><input type="radio" name="scope" value="until-ok" defaultChecked /><span><b>Until it passes again</b><span className="muted">Ends at the next passing check, at most {o.openDays} days.</span></span></label>
                  <label className="opt"><input type="radio" name="scope" value="days" /><span><b>For a number of days</b><span className="muted">Uses the number below.</span></span></label>
                  <label className="opt"><input type="radio" name="scope" value="until-changed" /><span><b>Until the newest backup changes</b><span className="muted">Ends when a new object or size appears, at most {o.openDays} days.</span></span></label>
                </fieldset>
                <label className="field"><span>Days (1 to {o.maxDays})</span><input className="num-s" name="days" type="number" min={1} max={o.maxDays} defaultValue={7} /></label>
                <Button size="sm">Mark as OK</Button></form>
            </PlainCard>
          ) : null}
          <PlainCard as="section" aria-labelledby="sz-h"><div className="card-h"><h2 id="sz-h">Size of the newest backup, last 30 days</h2></div>
            {d.chart ? <><p className="legend"><span>latest <b>{d.chart.latest}</b></span><span>min <b>{d.chart.min}</b></span><span>max <b>{d.chart.max}</b></span><span>{full(d.chart.from)} – {full(d.chart.to)}</span></p>
              <Line points={d.chart.points} low={d.chart.low} high={d.chart.high} label={`Size of newest backup, ${d.name}`} /></>
              : <p className="empty"><b>Not enough history for a chart yet.</b> It appears after the second recorded run.</p>}
          </PlainCard>
          <PlainCard as="section" className="l-card" aria-labelledby="hs-h"><div className="card-h rows-h"><h2 id="hs-h">History</h2></div>
            <div className="tablewrap" role="region" aria-label="History table" tabIndex={0}><table className="l-table">
              <thead><tr><th scope="col">Checked</th><th scope="col">Status</th><th scope="col" className="num">Age</th><th scope="col" className="num">Size</th><th scope="col">Newest object</th></tr></thead>
              <tbody>{(d.rows ?? []).map((h, i) => <tr key={i}><td className="t-main nw"><When ts={h.at} /></td><td data-label="Status"><P p={h.status} /></td><td data-label="Age" className="num nw">{h.age}</td><td data-label="Size" className="num nw">{h.size}</td><td data-label="Newest object" className="mono">{h.newest}</td></tr>)}</tbody>
            </table></div></PlainCard>
        </div>
      )}
    </>
  );
}

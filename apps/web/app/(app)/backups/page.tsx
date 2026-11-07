import { HelpQ } from "@/components/HelpQ";
import { plural } from "@/lib/format";
import type { Metadata } from "next";
import { AcceptedNote, At, Bar, P, Reload, RestoreText, Scan } from "@/components/Infra";
import { Markdown } from "@/components/Markdown";
import { EnvPill, Pill } from "@/components/Pills";
import { Banners, Filters, pick, type Search, Tiles, tone } from "@/components/Section";
import { qs } from "@/lib/api";
import { apiGet, csrfToken, readFlash } from "@/lib/server";
import type { BackupRow, Backups, CoverItem } from "@/lib/types";
import { buttonVariants } from "@/components/ui/button";
import { PlainCard } from "@/components/ui/card";

export const metadata: Metadata = { title: "Backups" };

/** The check's codes in words; the code stays on the set's own page. */
const SET_WORD: Record<string, string> = { SMALL: "Too small", STALE: "No recent backup", MISSING: "None found" };

function Sets({ rows, csrf, label }: { rows: BackupRow[]; csrf: string; label: string }) {
  return (
    <div className="tablewrap" role="region" aria-label={label} tabIndex={0}><table className="l-table">
      <thead><tr><th scope="col">Status</th><th scope="col">Set</th><th scope="col" className="num">Age</th><th scope="col" className="num">Size</th><th scope="col" className="num" title="This backup's size as a percentage of the median of the last 7">Size vs usual</th><th scope="col" className="num">Kinds</th><th scope="col">Newest object</th><th scope="col">Restore tested</th></tr></thead>
      <tbody>{rows.map((x) => (
        <tr key={x.name}>
          <td>{x.accepted ? <><Pill tone="info" title={`${x.status.text} accepted by ${x.accepted.by}`}>Accepted</Pill><span className="sub">still <span className="mono">{x.status.text}</span></span></> : <P p={{ ...x.status, text: SET_WORD[x.status.text] ?? x.status.text }} />}</td>
          <td className="t-main"><a href={x.href}>{x.name}</a>{x.mark ? <span className="sub"><a href={x.href + "#accept"}>Mark as OK</a></span> : null}{x.accepted ? <AcceptedNote a={x.accepted} back="/backups" csrf={csrf} /> : null}</td>
          <td data-label="Age" className="num nw">{x.age}</td><td data-label="Size" className="num nw">{x.size}</td><td data-label="Size vs usual" className="num">{x.ratio}</td><td data-label="Kinds" className="num">{x.kinds}</td>
          <td data-label="Newest object" className="mono">{x.newest}</td><td data-label="Restore tested"><RestoreText r={x.restore} /></td>
        </tr>
      ))}</tbody>
    </table></div>
  );
}

function Items({ list }: { list: CoverItem[] }) {
  return <ul className="rows">{list.map((i, n) => (
    <li key={n}><p className="th"><Pill tone={tone(i.kind)}>{i.risk}</Pill><b dir="auto">{i.title}</b></p><p className="muted small">{i.meta}</p>
      {i.board.href ? <p className="small"><a href={i.board.href}>{i.board.text}</a></p> : null}</li>
  ))}</ul>;
}

function Panel({ title, list, note }: { title: string; list: CoverItem[]; note?: string }) {
  return <PlainCard as="section" className="l-card" aria-label={title}><div className="card-h rows-h"><h3 className="sec-h">{title} <span className="ncount">{list.length}</span></h3>{note ? <span className="muted small">{note}</span> : null}</div>
    {list.length ? <Items list={list} /> : <p className="empty">None.</p>}</PlainCard>;
}

export default async function BackupsPage({ searchParams }: { searchParams: Search }) {
  const params = await pick(searchParams, ["status", "bucket", "sent"]);
  const [r, csrf, flash] = await Promise.all([apiGet<Backups>("/api/v1/backups" + qs(params)), csrfToken(), readFlash()]);
  if (r.status !== 200) throw new Error(`/api/v1/backups answered ${r.status}`);
  const d = r.data;
  const c = d.coverage;
  const rows = d.rows ?? [];
  const okRows = d.okRows ?? [];
  return (
    <>
      <Reload r={d.refresh} />
      <header className="phead"><div><h1>Backups</h1><HelpQ p="/backups" />
        {d.ran ? <p className="meta-line">{d.status ? <P p={d.status} /> : null}<span>{plural(d.sets ?? 0, "set")}</span><span>{d.failing} failing</span><span>checked <At ts={d.at ?? 0} /></span>{d.accepted ? <span>{d.accepted} accepted</span> : null}</p> : null}</div>
        <div className="phead-act"><Scan s={d.scan} csrf={csrf} /></div></header>
      <Banners list={d.banners} flash={flash} />
      <div className="actionbar">
        <a data-slot="button" className={buttonVariants({ variant: d.admin ? "default" : "outline" })} href="/backups/new" title={d.admin ? undefined : "Viewers can check a plan; admins save it"}>New backup</a>
        <a data-slot="button" className={buttonVariants({ variant: "outline" })} href="#coverage">Coverage</a>
        <a data-slot="button" className={buttonVariants({ variant: "outline" })} href={d.templates.url} rel="noopener noreferrer">Templates ({d.templates.repo})</a>
      </div>
      {!d.ran ? <p className="empty"><b>No runs yet.</b> No backup-check run recorded yet.</p> : (
        <>
          {d.summary ? <PlainCard as="details" className="fold-t"><summary>Agent summary</summary><div className="md"><Markdown text={d.summary} /></div></PlainCard> : null}
          {d.list && d.facets ? <div className="sec"><Filters list={d.list} facets={d.facets} label="Filter backup sets" placeholder="" /></div> : null}
          <PlainCard as="section" className="l-card" aria-label="Backup sets">
            {rows.length ? <Sets rows={rows} csrf={csrf} label="Backup sets table" /> : !okRows.length ? <p className="empty">{d.list?.filtered ? "No sets match this filter." : "No backup set in the last check."}</p> : <p className="empty">No failing set.</p>}
            {okRows.length ? <details className="fold-t fold-c"><summary>Sets OK <span className="ncount">{okRows.length}</span></summary><Sets rows={okRows} csrf={csrf} label="Sets OK" /></details> : null}
          </PlainCard>
        </>
      )}
      <div className="sec" id="coverage">
        <h2>Coverage</h2>
        <p className="l-note">From the read-only discovery of {c.generated}. Backup MinIO {c.host}: disk <b>{c.diskPct} %</b> ({c.used} of {c.total} TB{c.issue ? `, ${c.issue}` : ""}).</p>
        <span className="meter wide" title={`${c.used} of ${c.total} TB`}><Bar pct={c.pct} kind={c.diskKind} /></span>
        <div className="sec"><Tiles tiles={c.tiles} /></div>
        <div className="halves">
          <Panel title="High-risk defects" list={c.high} />
          <PlainCard as="section" className="l-card" aria-label="Families"><div className="card-h rows-h"><h3 className="sec-h">Families <span className="ncount">{c.families.length}</span></h3></div>
            <ul className="rows">{c.families.map((f) => <li key={f.id}><p className="th"><Pill>{f.id}</Pill><b>{f.name}</b><span className="muted small">{plural(f.sets, "set")}{f.size ? ` · ${f.size}` : ""}</span></p><p className="small" title={f.how}>{f.engine} · {f.how}</p><p className="muted small">defects {f.defects}{f.issues ? ` · ${f.issues}` : ""}</p></li>)}</ul>
            {(d.sets ?? 0) > c.families.reduce((n, f) => n + f.sets, 0) ? <p className="muted small pad">{plural((d.sets ?? 0) - c.families.reduce((n, f) => n + f.sets, 0), "set")} outside any family.</p> : null}</PlainCard>
          <Panel title="Databases without a backup" list={c.gaps} />
          <Panel title="Unencrypted sets" list={c.unencrypted} />
        </div>
        <div className="sec"><Panel title="Sets without a known script" list={c.noscript} note="family E: the host and the job are unknown" /></div>
      </div>
      <PlainCard as="section" className="l-card" id="plans" aria-labelledby="bp-h"><div className="card-h rows-h"><h2 id="bp-h">Backup plans <span className="ncount">{d.plans.length}</span></h2></div>
        {d.plans.length ? <div className="tablewrap" role="region" aria-label="Backup plans table" tabIndex={0}><table className="l-table">
          <thead><tr><th scope="col">State</th><th scope="col">Plan</th><th scope="col">Env</th><th scope="col">Server</th><th scope="col">Schedule</th><th scope="col">Destination</th><th scope="col">Restore tested</th></tr></thead>
          <tbody>{d.plans.map((p) => <tr key={p.id}><td><P p={p.state} /></td><td className="t-main"><a href={`/backups/plan?id=${p.id}`}>{p.name}</a><span className="sub">#{p.id} · {p.engine}</span></td><td data-label="Env"><EnvPill env={p.env} /></td><td data-label="Server" className="mono">{p.server}</td><td data-label="Schedule">{p.schedule}</td><td data-label="Destination" className="mono">{p.destination}</td><td data-label="Restore tested"><RestoreText r={p.restore} /></td></tr>)}</tbody>
        </table></div> : <p className="empty"><b>No plan yet.</b> New backup turns a form into a job directory, a merge request in {d.templates.repo} and a board task.</p>}
      </PlainCard>
    </>
  );
}

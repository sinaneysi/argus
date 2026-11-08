import { HelpQ } from "@/components/HelpQ";
import type { Metadata } from "next";
import { notFound } from "next/navigation";
import { seoSend } from "./actions";
import { At, Line, P, Reload } from "@/components/Infra";
import { Pill } from "@/components/Pills";
import { Banners, Hidden, pick, type Search } from "@/components/Section";
import { qs } from "@/lib/api";
import { full } from "@/lib/format";
import { apiGet, csrfToken, readFlash } from "@/lib/server";
import type { Seo, SeoChart, SeoWindow } from "@/lib/types";
import { Button } from "@/components/ui/button";
import { PlainCard } from "@/components/ui/card";

export const metadata: Metadata = { title: "SEO" };

function Chart({ id, title, c, empty }: { id: string; title: string; c: SeoChart | undefined; empty: string }) {
  return (
    <PlainCard as="section" aria-labelledby={id}><div className="card-h"><h3 id={id}>{title}</h3></div>
      {c ? <><p className="legend"><span>latest <b>{c.latest}</b></span><span>min <b>{c.min}</b></span><span>max <b>{c.max}</b></span><span>{full(c.from)} – {full(c.to)}</span></p>
        <Line points={c.points} low={c.low} high={c.high} label={title} /></> : <p className="empty">{empty}</p>}
    </PlainCard>
  );
}

function Win({ w, label }: { w: SeoWindow; label: string }) {
  if (!w) return <><td data-label={`${label} requests`} className="num muted">-</td><td data-label={`${label} 5xx`} className="num muted">-</td><td data-label={`${label} p95`} className="num muted">-</td></>;
  return <><td data-label={`${label} requests`} className="num">{w.requests}</td><td data-label={`${label} 5xx`} className="num nw">{w.share}</td><td data-label={`${label} p95`} className="num nw">{w.p95}</td></>;
}

export default async function SeoPage({ searchParams }: { searchParams: Search }) {
  const params = await pick(searchParams, ["sent"]);
  const [r, csrf, flash] = await Promise.all([apiGet<Seo>("/api/v1/seo" + qs(params)), csrfToken(), readFlash()]);
  if (r.status === 404) notFound(); // the marketing module is off (ADR-0021 D6)
  if (r.status !== 200) throw new Error(`/api/v1/seo answered ${r.status}`);
  const d = r.data;
  const s = d.suggestions;
  const g = d.gsc;
  const f = d.summary.findings;
  return (
    <>
      <Reload r={d.refresh} />
      <header className="phead"><div><h1>SEO</h1><HelpQ p="/seo" />
        {!d.empty && d.at ? <p className="status-line">Googlebot snapshot <At ts={d.at} /><span className="sep">·</span>refreshes every 60 s</p> : null}</div></header>
      <Banners list={d.banners} flash={flash} />

      <PlainCard as="section" className="l-card" aria-labelledby="sum-h">
        <div className="card-h rows-h"><h2 id="sum-h">Operations summary {d.summary.fix ? <span className="ncount">{d.summary.fix} to fix</span> : <Pill tone="ok">no action needed</Pill>}</h2></div>
        <p className="card-sub">Problems that hurt search and that operations can fix, worst first. From the Googlebot logs of the last hour and the Search Console URL and sitemap checks.</p>
        {f.length ? <div className="tablewrap" role="region" aria-label="Operations summary" tabIndex={0}><table className="l-table">
          <thead><tr><th scope="col">Level</th><th scope="col">Problem</th><th scope="col">What to do</th></tr></thead>
          <tbody>{f.map((x, i) => <tr key={i}><td><P p={x.level} /></td><td className="t-main"><b>{x.what}</b><span className="sub">{x.evidence}</span></td><td data-label="What to do">{x.action}</td></tr>)}</tbody>
        </table></div> : <p className="empty">No Googlebot 5xx in the last hour, no slow edge, key URLs indexed, sitemaps clean.</p>}
      </PlainCard>

      <div className="sec" id="suggestions">
        <h2>Suggestions {s.day ? <span className="muted small">{s.day}</span> : null}</h2>
        {s.empty ? <p className="empty"><b>No suggestions yet.</b> The SEO job builds them daily from Search Console.</p> : <>
          {s.summary?.length ? <PlainCard as="section" aria-labelledby="top-h"><div className="card-h"><h3 id="top-h">Top actions today</h3></div>
            <ul className="list-plain">{s.summary.map((l, i) => <li key={i} dir="auto">{l}</li>)}</ul>
            <p className="card-sub">A model wrote this from the tables below. Check the numbers there before you act.</p></PlainCard> : null}
          <p className="l-note">From Search Console (28 days, final data) and analytics when it answers. Rules: clicks falling in the last 7 days against the 21 days before, low CTR for the main page of a query, queries at position 4 to 15 with real demand, and high bounce from Google. Brand and off-topic queries are left out.</p>
          <form action={seoSend} className="ctl-f"><Hidden csrf={csrf} /><Button variant="outline" size="sm" title="Post today's top suggestions to the chat room">Send to chat</Button></form>
          {(s.groups ?? []).map((gr) => (
            <PlainCard key={gr.key} as="section" className="l-card" aria-labelledby={`sg-${gr.key}`}>
              <div className="card-h rows-h"><h3 id={`sg-${gr.key}`}>{gr.title} <span className="ncount">{gr.rows.length}</span></h3></div>
              <div className="tablewrap" role="region" aria-label={gr.title} tabIndex={0}><table className="l-table">
                <thead><tr><th scope="col">What</th><th scope="col">Evidence</th><th scope="col">What to do</th></tr></thead>
                <tbody>{gr.rows.map((x, i) => <tr key={i}><td className="t-main"><b dir="auto">{x.title}</b><span className="sub mono">{x.page}</span></td><td data-label="Evidence">{x.evidence}</td><td data-label="What to do" dir="auto">{x.action}</td></tr>)}</tbody>
              </table></div>
            </PlainCard>
          ))}
        </>}
      </div>

      <div className="sec" id="googlebot">
        <h2>Googlebot per edge {d.level ? <P p={d.level} /> : null}</h2>
        {d.empty ? <p className="empty"><b>No Googlebot snapshot yet.</b> The SEO job writes one every 15 minutes.</p> : <>
          <PlainCard as="section" className="l-card" aria-label="Googlebot per edge">
            <div className="tablewrap" role="region" aria-label="Googlebot per edge table" tabIndex={0}><table className="l-table">
              <thead>
                <tr><th scope="col" rowSpan={2}>Level</th><th scope="col" rowSpan={2}>Edge · host</th><th scope="colgroup" colSpan={5}>Now (15 min)</th><th scope="colgroup" colSpan={3}>1 day ago</th><th scope="colgroup" colSpan={3}>7 days ago</th></tr>
                <tr><th scope="col" className="num">Requests</th><th scope="col" className="num">5xx</th><th scope="col" className="num">p95</th><th scope="col" className="num" title="Share from Google's crawler address range">Google IP</th><th scope="col" className="num">Cache HIT</th>
                  <th scope="col" className="num">Requests</th><th scope="col" className="num">5xx</th><th scope="col" className="num">p95</th><th scope="col" className="num">Requests</th><th scope="col" className="num">5xx</th><th scope="col" className="num">p95</th></tr>
              </thead>
              <tbody>{(d.edges ?? []).map((e, i) => (
                <tr key={i}><td><P p={e.level} /></td><td className="t-main nw"><b>{e.edge}</b>{e.cache ? <> <Pill>cache edge</Pill></> : null}<span className="sub">{e.host}</span></td>
                  <Win w={e.now} label="Now" /><td data-label="Google IP" className="num muted">{e.verified}</td><td data-label="Cache HIT" className="num muted">{e.hits}</td>
                  <Win w={e.day} label="1 day ago" /><Win w={e.week} label="7 days ago" /></tr>
              ))}</tbody>
            </table></div>
          </PlainCard>
          <p className="l-note">Edge access logs, requests whose user agent says Googlebot. Google IP is the share from Google&apos;s crawler range 66.249.64.0/19; the rest only claim the name. FAIL: 5xx at 5 % or more, or p95 at 3 times the 7-day value. WARN: 1 % or 2 times. A cache edge also shows its cache hit share.</p>
          <div className="halves">
            <Chart id="ch-share" title="Googlebot 5xx share, 7 days" c={d.charts?.share} empty="Not enough snapshots for a chart yet." />
            <Chart id="ch-p95" title="Googlebot p95 request time, worst edge, 7 days" c={d.charts?.p95} empty="Not enough snapshots for a chart yet." />
          </div>
        </>}
      </div>

      <div className="sec" id="search-console">
        <h2>Google Search Console</h2>
        {g.empty ? <p className="empty"><b>No Search Console data yet.</b> The Search Console job reads it every 6 hours.</p> : <>
          <p className="l-note">Google&apos;s own final numbers. A day appears 2 to 3 days later.</p>
          <div className="halves">
            <Chart id="ch-cl" title="Clicks per day" c={g.clicks} empty="Not enough days for a chart yet." />
            <Chart id="ch-im" title="Impressions per day" c={g.impressions} empty="Not enough days for a chart yet." />
            <Chart id="ch-po" title="Average position (lower is better)" c={g.position} empty="Not enough days for a chart yet." />
          </div>
          {g.urls?.length ? <PlainCard as="section" className="l-card" aria-labelledby="urls-h"><div className="card-h rows-h"><h3 id="urls-h">Index state of key URLs <span className="ncount">{g.urls.length}</span></h3></div>
            <div className="tablewrap" role="region" aria-label="Index state of key URLs" tabIndex={0}><table className="l-table">
              <thead><tr><th scope="col">State</th><th scope="col">URL</th><th scope="col">Coverage</th><th scope="col">Last crawl</th><th scope="col">Crawled as</th></tr></thead>
              <tbody>{g.urls.map((u, i) => <tr key={i}><td><P p={u.level} /></td><td className="t-main mono">{u.url}</td><td data-label="Coverage">{u.coverage}</td><td data-label="Last crawl" className="muted nw">{u.lastCrawl || "-"}</td><td data-label="Crawled as" className="muted">{u.crawledAs || "-"}</td></tr>)}</tbody>
            </table></div></PlainCard> : null}
        </>}
      </div>
    </>
  );
}

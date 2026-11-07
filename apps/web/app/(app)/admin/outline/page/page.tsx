import type { Metadata } from "next";
import { notFound } from "next/navigation";
import { AdminsOnly } from "@/components/Agent";
import { Markdown } from "@/components/Markdown";
import { Crumb, one, type Search } from "@/components/Section";
import "@/components/section.css";
import "@/components/settings.css";
import { qs } from "@/lib/api";
import { apiGet } from "@/lib/server";
import type { OutlinePage } from "@/lib/types";
import { PlainCard } from "@/components/ui/card";

export const metadata: Metadata = { title: "Outline page" };

/** One built Outline page: its facts, the changes since the previous build, and the page as Outline will show it. */
export default async function OutlinePageView({ searchParams }: { searchParams: Search }) {
  const s = one((await searchParams).s);
  const r = await apiGet<OutlinePage>("/api/v1/admin/outline/page" + qs({ s }));
  if (r.status === 403) return <><header className="phead"><div><h1>Outline page</h1></div></header><AdminsOnly /></>;
  if (r.status === 404) notFound();
  if (r.status !== 200) throw new Error(`/api/v1/admin/outline/page answered ${r.status}`);
  const d = r.data;
  return (
    <>
      <header className="phead"><div><Crumb href="/admin/outline#pages" label="Integrations" /><h1>{d.name}</h1><p className="psub">{d.mode === "publish" ? "Published" : "Preview"} page · <span className="mono">{d.hash}</span></p></div></header>
      <div className="stack">
        <PlainCard as="section" aria-label="Page facts"><dl className="set-kv">
          <div><dt>Title in Outline</dt><dd>{d.title}</dd></div>
          <div><dt>Built</dt><dd>{d.built}</dd></div>
          <div><dt>Content changed</dt><dd>{d.changed}</dd></div>
          <div><dt>Secret scan</dt><dd className={d.blocked ? "ol-blocked" : undefined}>{d.scan}</dd></div>
          <div><dt>In Outline</dt><dd>{d.outline.url ? <a href={d.outline.url} rel="noopener noreferrer">{d.outline.label}</a> : <span className="muted">{d.outline.label}</span>}</dd></div>
          {d.flags.length ? <div><dt>Flags</dt><dd>{d.flags.join(", ")}</dd></div> : null}
        </dl></PlainCard>
        <PlainCard as="section" aria-labelledby="ol-diff-h"><div className="card-h"><h2 id="ol-diff-h">Changes since the previous build</h2></div>
          {!d.diff ? <p className="set-note">First build of this page: everything is new.</p> : !d.diff.lines.length ? <p className="set-note">No difference.</p> : <>
            <pre className="code ol-diff" role="region" aria-label="Changes since the previous build" tabIndex={0}>{d.diff.lines.map((l, i) => <span key={i} className={l.kind || undefined}>{l.text || " "}</span>)}</pre>
            {d.diff.more ? <p className="set-note">{d.diff.more} more diff lines.</p> : null}</>}
        </PlainCard>
        <PlainCard as="section" aria-labelledby="ol-view-h"><div className="card-h"><h2 id="ol-view-h">Page as Outline will show it</h2></div>
          <p className="set-note">Argus&apos;s block only; the page in Outline also keeps a Team notes section that people write.</p>
          <article className="md"><Markdown text={d.view} local /></article>
          <details className="fold"><summary>Markdown sent to Outline</summary><pre className="code" role="region" aria-label="Markdown sent to Outline" tabIndex={0}><code>{d.body}</code></pre></details>
        </PlainCard>
      </div>
    </>
  );
}

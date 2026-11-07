import type { Metadata } from "next";
import { AdminTag, HelpBody, HelpSearch } from "@/components/Help";
import { pick, type Search } from "@/components/Section";
import { qs } from "@/lib/api";
import { plural } from "@/lib/format";
import { apiGet } from "@/lib/server";
import type { HelpIndex } from "@/lib/types";

const HOME = "/help";

export async function generateMetadata({ searchParams }: { searchParams: Search }): Promise<Metadata> {
  return { title: (await searchParams).q ? "Search · Help" : "Help" };
}

export default async function HelpPage({ searchParams }: { searchParams: Search }) {
  const r = await apiGet<HelpIndex>("/api/v1/help" + qs(await pick(searchParams, ["q"])));
  if (r.status !== 200) throw new Error(`/api/v1/help answered ${r.status}`);
  const d = r.data;
  const res = d.results;
  return (
    <>
      <header className="phead"><div><h1>Help</h1><p className="psub">What Argus can do, where to find it and how to use it</p></div></header>
      <HelpSearch q={d.q} max={d.maxQuery} />
      {res ? (
        res.count ? (
          <>
            <p className="h-count">{plural(res.count, "match", "matches")} for ‘{d.q}’ · <a href={HOME}>Clear the search</a></p>
            <ul className="h-hits">{res.rows.map((h) => (
              <li key={h.href}><a href={h.href}>{h.page}{h.section ? <> <span className="muted">›</span> {h.section}</> : null}</a>
                <p dir="auto">{h.snippet.map((s, i) => (s.mark ? <mark key={i}>{s.text}</mark> : s.text))}</p></li>
            ))}</ul>
            {res.count > res.shown ? <p className="muted small">Showing the first {res.shown} of {res.count} matches. Add a word to narrow it.</p> : null}
          </>
        ) : <p className="empty"><b>Nothing found for ‘{d.q}’.</b> Try one word, or a page or button name. <a href={HOME}>All help pages</a></p>
      ) : (
        <>
          {d.home ? <article className="md h-doc h-home"><HelpBody doc={d.home} /></article> : null}
          <ul className="h-cards">{(d.cards ?? []).map((c) => (
            <li key={c.href} className="card"><h2><a href={c.href}>{c.title}</a>{c.admin ? <AdminTag /> : null}</h2><p>{c.summary}</p>
              <p className="h-card-s">{c.sections.map((s) => <a key={s.value} href={s.value}>{s.label}</a>)}</p></li>
          ))}</ul>
        </>
      )}
    </>
  );
}

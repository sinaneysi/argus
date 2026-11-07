import type { Metadata } from "next";
import { notFound } from "next/navigation";
import { Reload } from "@/components/Infra";
import { Banners, Crumb, pick, type Search, When } from "@/components/Section";
import { ChatBox } from "@/components/Work";
import { qs } from "@/lib/api";
import { apiGet, csrfToken, readFlash } from "@/lib/server";
import type { Ask } from "@/lib/types";
import { PlainCard } from "@/components/ui/card";

export const metadata: Metadata = { title: "Ask Argus" };

export default async function AskPage({ searchParams }: { searchParams: Search }) {
  const params = await pick(searchParams, ["key", "req"]);
  const [r, csrf, flash] = await Promise.all([apiGet<Ask>("/api/v1/board/ask" + qs(params)), csrfToken(), readFlash()]);
  if (r.status === 404) notFound();
  if (r.status !== 200) throw new Error(`/api/v1/board/ask answered ${r.status}`);
  const d = r.data;
  return (
    <>
      <Reload r={d.refresh} />
      <header className="phead"><div><Crumb href={d.thread ? "/board/ask" : "/board"} label={d.thread ? "All threads" : "Board"} /><h1>Ask Argus</h1>
        <p className="status-line">Questions and new tasks; a task Argus defines waits in <a href="/review?kind=proposals">Review</a> until an admin accepts it</p></div></header>
      <Banners list={d.notices} flash={flash} />
      {!d.on ? <PlainCard as="section"><p className="empty">The task chat is off. An admin turns it on in Admin → Task agent.</p></PlainCard> : null}
      <div className="stack">
        {d.chat ? <ChatBox c={d.chat} csrf={csrf} ret={d.return} /> : null}
        {d.on && !d.thread ? (
          <PlainCard as="section" className="l-card" aria-labelledby="th-h">
            <div className="card-h rows-h"><h2 id="th-h">Recent threads</h2></div>
            {d.threads.length ? <ul className="rows">{d.threads.map((t) => <li key={t.href}><p dir="auto"><a className="q-th" href={t.href}>{t.first}</a></p><p className="muted small"><span dir="auto">{t.who}</span> · {t.count} {t.count === 1 ? "message" : "messages"} · <When ts={t.at} /></p></li>)}</ul>
              : <p className="empty pad">No threads yet.</p>}
          </PlainCard>
        ) : null}
      </div>
    </>
  );
}

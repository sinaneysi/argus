import type { Metadata } from "next";
import { Markdown } from "@/components/Markdown";
import "@/components/agent.css";
import "@/components/section.css";
import { plural } from "@/lib/format";
import { apiGet } from "@/lib/server";
import type { Knowledge } from "@/lib/types";
import { PlainCard } from "@/components/ui/card";

export const metadata: Metadata = { title: "What Argus knows" };

export default async function KnowledgePage() {
  const r = await apiGet<Knowledge>("/api/v1/knowledge");
  if (r.status !== 200) throw new Error(`/api/v1/knowledge answered ${r.status}`);
  const d = r.data;
  return (
    <>
      <header className="phead"><div><h1>What Argus knows</h1><p className="meta-line">{d.count} approved {plural(d.count, "lesson")}</p></div></header>
      <div className="detail">
        <PlainCard as="article">{d.count ? <div className="md"><Markdown text={d.md.replace(/^_(.+)_$/gm, "$1")} /></div> : <p className="empty"><b>Nothing yet.</b> Argus&apos;s runs propose lessons; they appear here once an admin approves them.</p>}</PlainCard>
        <aside>
          <PlainCard as="section" className="rail-c"><h2>About this page</h2><p className="muted small">Generated from the lessons an admin approved. Runs read the relevant ones as context, never as instructions. It changes with every review.</p>
            {d.admin ? <p className="small"><a className="k-link" href="/review?kind=lessons">Review lessons</a></p> : null}</PlainCard>
          {d.services.length ? <PlainCard as="section" className="rail-c"><h2>Services</h2><ul className="plain">{d.services.map((s) => <li key={s} dir="auto">{s}</li>)}</ul></PlainCard> : null}
        </aside>
      </div>
    </>
  );
}

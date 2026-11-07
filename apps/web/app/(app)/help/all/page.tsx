import type { Metadata } from "next";
import { HelpBody } from "@/components/Help";
import { Crumb } from "@/components/Section";
import { apiGet } from "@/lib/server";
import type { HelpAll } from "@/lib/types";

export const metadata: Metadata = { title: "All pages · Help" };

export default async function HelpAllPage() {
  const r = await apiGet<HelpAll>("/api/v1/help/all");
  if (r.status !== 200) throw new Error(`/api/v1/help/all answered ${r.status}`);
  return (
    <>
      <header className="phead"><div><Crumb href="/help" label="Help" /><h1>Argus help, all pages</h1><p className="psub">Every help page on one page, to read, print or save as PDF</p></div></header>
      <nav className="h-all-toc" aria-label="Pages"><ol className="h-toc">{r.data.docs.map((d) => <li key={d.slug}><a href={`#p-${d.slug}`}>{d.title}</a></li>)}</ol></nav>
      {r.data.docs.map((d) => (
        <article key={d.slug} id={`p-${d.slug}`} className="md h-doc h-all">
          <h2 className="h-page-t">{d.title}</h2><p className="h-sum">{d.summary}</p>
          <HelpBody doc={d} open />
        </article>
      ))}
    </>
  );
}

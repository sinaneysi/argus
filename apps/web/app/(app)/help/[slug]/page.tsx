import type { Metadata } from "next";
import { notFound } from "next/navigation";
import { HelpBody, HelpSearch, HelpToc } from "@/components/Help";
import { Crumb } from "@/components/Section";
import { qs } from "@/lib/api";
import { apiGet } from "@/lib/server";
import type { HelpPage } from "@/lib/types";
import { PlainCard } from "@/components/ui/card";

const ALL = "/help/all";
type Params = { params: Promise<{ slug: string }> };
const read = async (p: Params["params"]) => apiGet<HelpPage>("/api/v1/help/page" + qs({ slug: (await p).slug }));

export async function generateMetadata({ params }: Params): Promise<Metadata> {
  const r = await read(params);
  return { title: r.status === 200 ? `${r.data.doc.title} · Help` : "Help" };
}

export default async function HelpDocPage({ params }: Params) {
  const r = await read(params);
  if (r.status === 404) notFound();
  if (r.status === 403) {
    return (
      <>
        <header className="phead"><div><Crumb href="/help" label="Help" /><h1>Admins only</h1></div></header>
        <p className="empty"><b>This help page is for admins.</b> It describes Settings, which only admins can open.</p>
      </>
    );
  }
  if (r.status !== 200) throw new Error(`/api/v1/help/page answered ${r.status}`);
  const { doc, pages, maxQuery } = r.data;
  return (
    <>
      <header className="phead"><div><Crumb href="/help" label="Help" /><h1>{doc.title}</h1><p className="psub">{doc.summary}</p></div></header>
      <div className="detail">
        <PlainCard as="article" className="md h-doc">
          {doc.sections.length ? <details className="h-toc-m fold-t"><summary>On this page</summary><HelpToc doc={doc} /></details> : null}
          <HelpBody doc={doc} />
        </PlainCard>
        <aside className="h-rail" aria-label="Help pages">
          <HelpSearch max={maxQuery} />
          <PlainCard as="section" className="rail-c">
            {doc.sections.length ? <><h2>On this page</h2><HelpToc doc={doc} /></> : null}
            <h2>Help pages</h2>
            <ul className="h-pages">{pages.map((p) => <li key={p.slug}><a href={p.href} aria-current={p.slug === doc.slug ? "page" : undefined}>{p.title}</a></li>)}</ul>
            <p className="h-print"><a href={ALL}>Printable version, all pages</a></p>
          </PlainCard>
        </aside>
      </div>
    </>
  );
}

import "./section.css";
import "./help.css";
import { Icon } from "./Icon";
import { Inline, Markdown } from "./Markdown";
import type { HelpDoc, HelpNode } from "@/lib/types";
import { Button } from "@/components/ui/button";

// The help pages, server components: the search is a GET form and the folds are <details>. The API has
// already dropped what a viewer may not read; the files are ours, so [label](/path) links are on.

export function HelpSearch({ q, max }: { q?: string; max: number }) {
  return (
    <form className="filters h-search" method="get" action="/help" role="search" aria-label="Search all help pages">
      <label className="f-search"><Icon name="search" /><span className="sr">Search all help pages</span>
        <input type="search" name="q" defaultValue={q} maxLength={max} placeholder="A word, a page or a button" autoComplete="off" enterKeyHint="search" /></label>
      <Button variant="outline">Search</Button>
    </form>
  );
}

const AdminTag = () => <span className="pill neutral">Admins</span>;

function Nodes({ list, open }: { list: HelpNode[]; open?: boolean }) {
  return <>{list.map((n, i) => {
    if (n.kind === "md") return <Markdown key={i} text={n.text} local />;
    if (n.kind === "h3") return <h3 key={i} id={n.id} dir="auto"><Inline text={n.text} local /></h3>;
    if (n.kind === "details") {
      return <details key={i} className="h-fold fold-t" open={open}><summary dir="auto"><Inline text={n.text} local /></summary>
        <div className="h-fold-b"><Nodes list={n.kids ?? []} open={open} /></div></details>;
    }
    return <div key={i} className="h-admin"><p className="h-admin-k">For admins</p><Nodes list={n.kids ?? []} open={open} /></div>;
  })}</>;
}

/** Intro and sections of one page; `open` unfolds every question (the printable page). */
export function HelpBody({ doc, open }: { doc: HelpDoc; open?: boolean }) {
  return (
    <>
      <Nodes list={doc.intro} open={open} />
      {doc.sections.map((s) => (
        <section key={s.id} id={s.id} className="h-sec">
          <h2 dir="auto"><Inline text={s.title} local />{s.admin ? <AdminTag /> : null}</h2>
          <Nodes list={s.nodes} open={open} />
        </section>
      ))}
    </>
  );
}

export function HelpToc({ doc }: { doc: HelpDoc }) {
  return <ol className="h-toc">{doc.sections.map((s) => <li key={s.id}><a href={`#${s.id}`}>{s.title}</a></li>)}</ol>;
}

export { AdminTag };

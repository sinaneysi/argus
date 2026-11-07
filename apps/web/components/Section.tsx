import "./section.css";
import { Banner } from "./Banner";
import { Icon } from "./Icon";
import { Inline, Markdown } from "./Markdown";
import { EnvPill, Pill } from "./Pills";
import { Button, buttonVariants } from "./ui/button";
import { Card, TitledCard } from "./ui/card";
import { Field, Input, Label, Select } from "./ui/field";
import { problemAct } from "@/app/(app)/incidents/actions";
import { ago, full, iso } from "@/lib/format";
import type { Banner as B, Facet, Fact, Fix, Lazy, ListState, MdDoc, Option, ProblemPanel, ProbItem, ProbLink, ProbSuggestion, Seg, Tile, TimelineEv } from "@/lib/types";
import { GetForm, SoftLink } from "./Soft";

// The parts the Incidents pages share (DESIGN.md "Queue page", "Detail page"). All server components: forms post
// through server actions and folds are <details>, so everything works without client JavaScript.

export type Search = Promise<Record<string, string | string[] | undefined>>;
export const one = (v: string | string[] | undefined): string => (Array.isArray(v) ? v[0] : v) ?? "";

/** The query keys a page passes to the API. */
export async function pick(sp: Search, keys: string[]): Promise<Record<string, string>> {
  const s = await sp;
  const out: Record<string, string> = {};
  for (const k of keys) if (one(s[k])) out[k] = one(s[k]);
  return out;
}

type Tone = "ok" | "warn" | "fail" | "info" | "neutral";
export const tone = (k?: string): Tone => (k === "ok" || k === "warn" || k === "fail" || k === "info" ? k : "neutral");

export function Segs({ s }: { s: Seg[] }) {
  return <>{s.map((x, i) => (x.href ? <a key={i} href={x.href} rel={x.href.startsWith("http") ? "noopener noreferrer" : undefined}>{x.text}</a> : <span key={i}>{x.text}</span>))}</>;
}

export function Banners({ list, flash }: { list?: B[]; flash?: B | null }) {
  return <>{flash ? <Banner b={flash} /> : null}{(list ?? []).map((b, i) => <Banner key={i} b={b} />)}</>;
}

export function Crumb({ href, label }: { href: string; label: string }) {
  return <a className="crumb-back" href={href}><Icon name="back" size={14} /> {label}</a>;
}

export function Refresh({ on }: { on?: boolean }) {
  return on ? <meta httpEquiv="refresh" content="5" /> : null;
}

/** `compact` sets the figure beside its label: for lists where the table is the page, not the tiles. */
export function Tiles({ tiles, compact }: { tiles: Tile[]; compact?: boolean }) {
  return (
    <div className={compact ? "tiles compact" : "tiles"}>
      {tiles.map((t) => {
        const body = <><span className="t-k">{t.label}</span><b className={t.hot ? "t-f num hot" : "t-f num"}>{t.figure}</b><span className="t-s">{t.sub}</span></>;
        return t.href ? <a key={t.label} className="tile" href={t.href} aria-current={t.current ? "true" : undefined}>{body}</a> : <div key={t.label} className="tile">{body}</div>;
      })}
    </div>
  );
}

/** Search, facets and sort as one GET form; every choice shows how many it would leave. */
export function Filters({ list, facets, label, placeholder, children }: { list: ListState; facets: Facet[]; label: string; placeholder: string; children?: React.ReactNode }) {
  return (
    <GetForm className="filters" action={list.path} role="search" aria-label={label}>
      {Object.entries(list.hidden).map(([k, v]) => <input key={k} type="hidden" name={k} value={v} />)}
      {list.noSearch ? null : <label className="f-search"><Icon name="search" /><span className="sr">Search</span>
        <input name="q" defaultValue={list.q} maxLength={100} placeholder={placeholder} autoComplete="off" /></label>}
      {facets.map((f) => (
        <label key={f.key} className={f.any && f.value ? "f-pill on" : "f-pill"}>
          <span className="f-l">{f.label}</span>
          <select name={f.key} defaultValue={f.value}>
            {f.any ? <option value="">Any</option> : null}
            {f.options.map((o) => <option key={o.value} value={o.value}>{o.label} ({o.count})</option>)}
          </select>
        </label>
      ))}
      {list.sorts.length ? (
        <label className="f-pill"><span className="f-l">Sort</span>
          <select name={list.sortKey ?? "sort"} defaultValue={list.sort}>{list.sorts.map((o) => <option key={o.value} value={o.value}>{o.label}</option>)}</select></label>
      ) : null}
      {children}
      <Button variant="outline">Apply</Button>
      {list.filtered ? <SoftLink data-slot="button" className={buttonVariants({ variant: "ghost", size: "icon" })} href={list.clearHref} title="Reset the filters" aria-label="Reset the filters"><Icon name="reset" /></SoftLink>
        : <Button type="button" variant="ghost" size="icon" disabled title="No filter to reset" aria-label="Reset the filters"><Icon name="reset" /></Button>}
      {/* the bar fits one line at 1440px: the result count is read out, not shown (the pager and the facet counts show it) */}
      <span className="sr" role="status">{list.filtered ? `${list.matched} of ${list.total}` : list.matched} {list.noun}</span>
    </GetForm>
  );
}

export function Pager({ list }: { list: ListState }) {
  if (list.pages <= 1) return null;
  return (
    <nav className="l-pager" aria-label="Pages">
      <span className="muted num">{list.from}–{list.to} of {list.matched} · page {list.page} of {list.pages}</span>
      {list.prev ? <a data-slot="button" className={buttonVariants({ variant: "outline", size: "sm" })} rel="prev" href={list.prev}>Previous</a> : null}
      {list.next ? <a data-slot="button" className={buttonVariants({ variant: "outline", size: "sm" })} rel="next" href={list.next}>Next</a> : null}
    </nav>
  );
}

export function When({ ts }: { ts: number }) {
  return ts ? <time dateTime={iso(ts)} title={full(ts)}>{ago(ts)}</time> : <span className="muted">-</span>;
}

export function Rail({ facts, title = "Record", id }: { facts: Fact[]; title?: string; id?: string }) {
  return (
    <Card as="section" className="rail-c block px-(--card-spacing)" id={id}>
      <h2>{title}</h2>
      <dl className="rec">
        {facts.map((f) => (
          <div key={f.label}><dt>{f.label}</dt><dd className={f.mono ? "mono" : undefined} dir="auto">{f.href ? <a href={f.href} rel={f.href.startsWith("http") ? "noopener noreferrer" : undefined}>{f.value}</a> : f.value}</dd></div>
        ))}
      </dl>
    </Card>
  );
}

export function Jump({ items }: { items: Option[] }) {
  if (items.length < 2) return null;
  return <nav className="jump" aria-label="On this page">{items.map((o) => <a key={o.value} href={"#" + o.value}>{o.label}</a>)}</nav>;
}

/** The plain-words card on top of a detail page. */
export function Summary({ l }: { l: Lazy | null }) {
  if (!l) return null;
  return (
    <TitledCard id="lazy-reading" titleId="lz-h" className="lazy" title="Summary" action={<span className="muted small">{l.source}</span>}>
      <dl className="rec wide">
        {l.rows.map((r) => <div key={r.label}><dt>{r.label}</dt><dd dir="auto"><Inline text={r.value} codeOnly /></dd></div>)}
        <div><dt>Fix status</dt><dd><Pill tone={tone(l.statusKind)}>{l.status}</Pill></dd></div>
      </dl>
    </TitledCard>
  );
}

/** A report body; behind "Full analysis" when the Summary card already says it in short. */
export function Report({ doc, folded, ids }: { doc: MdDoc; folded?: boolean; ids?: boolean }) {
  const body = (
    <div className="md">
      <Markdown text={doc.md} ids={ids} />
      {doc.foldOptions || doc.foldPrompt ? (
        <details className="fold"><summary>How to fix it and the prompt for a write-access agent</summary>
          {doc.foldOptions ? <Markdown text={doc.foldOptions} /> : null}
          {doc.foldPrompt ? <pre className="code"><code>{doc.foldPrompt}</code></pre> : null}
        </details>
      ) : null}
    </div>
  );
  return folded ? <details className="fold" id="full-analysis"><summary>Full analysis</summary>{body}</details> : body;
}

export function FixCards({ f, quiet }: { f: Fix; quiet?: boolean }) {
  return (
    <>
      <TitledCard id="how-to-fix" titleId="fx-h" title={<>How to fix it {f.options.length ? <span className="ncount">{f.options.length}</span> : null}</>}>
        {f.note.length ? <p className="card-sub"><Segs s={f.note} /></p> : null}
        {f.options.length ? (
          <ol className="fixopts">
            {f.options.map((o) => (
              <li key={o.n}>
                <p><span className="num muted">{o.n}.</span> <b dir="auto"><Inline text={o.title} codeOnly /></b> {o.recommended ? <Pill tone="ok">Recommended</Pill> : null}</p>
                <p className="fo-tags">{o.kind ? <Pill>{o.kind}</Pill> : null}{o.approval ? <Pill title={o.approvalTitle}>{o.approval}</Pill> : null}</p>
                {o.what ? <p dir="auto"><Inline text={o.what} codeOnly /></p> : null}
                {o.kv.length ? <dl className="rec wide">{o.kv.map((k) => <div key={k.label}><dt>{k.label}</dt><dd dir="auto"><Inline text={k.value} codeOnly /></dd></div>)}</dl> : null}
              </li>
            ))}
          </ol>
        ) : <p className="empty"><b>Not generated.</b> This RCA has no &quot;How to fix it&quot; section: it was written before Argus asked every RCA for fix options, or the run left it out.</p>}
      </TitledCard>
      <TitledCard id="fix-prompt" titleId="fp-h" title="Prompt for a write-access agent" action={f.promptHref ? <a data-slot="button" className={buttonVariants({ variant: "outline", size: "sm" })} href={f.promptHref} download>Download .md</a> : null}>
        {f.prompt ? (
          <>
            <p className="card-sub">Self-contained: the goal, the targets, the evidence, the guardrails, every step with its backup and rollback, and what to report back. Review it first.</p>
            <pre className="code prompt" role="region" aria-label="Prompt text" tabIndex={0}><code>{f.prompt}</code></pre>
            {f.checks.length ? <p className="muted small">Checked by Argus: {f.checks.join(" · ")}</p> : null}
            {f.prod ? <p><Pill tone="neutral">proposal: operator runs it</Pill></p> : null}
          </>
        ) : <p className="empty"><b>Not generated.</b> There is no prompt for an agent with write access yet. It is written with the RCA; older RCAs of open incidents get one from Argus&apos;s regeneration.</p>}
        {f.releaseNote ? <p className="muted small">{f.releaseNote}</p> : null}
        {f.action ? (
          <div className="fx-act">
            {f.action.heading ? <h3>{f.action.heading}</h3> : null}
            <p className="muted"><Segs s={f.action.body} /></p>
            {f.action.button?.href ? <a data-slot="button" className={buttonVariants({ variant: quiet ? "outline" : "default" })} href={f.action.button.href}>{f.action.button.text}</a> : null}
          </div>
        ) : null}
      </TitledCard>
    </>
  );
}

const OPEN = 12;

function Events({ list }: { list: TimelineEv[] }) {
  return (
    <ol className="tl">
      {list.map((e, i) => (
        <li key={i} className={`ev-${e.kind}`}>
          <p className="tl-h"><time dateTime={iso(e.at)}>{full(e.at)}</time> <span className="muted"><Segs s={e.source} /></span></p>
          {e.title ? <p dir="auto"><b>{e.title}</b></p> : null}
          <p dir="auto"><Segs s={e.detail} /></p>
        </li>
      ))}
    </ol>
  );
}

/** The newest events open, the older ones folded. */
export function Timeline({ list, note }: { list: TimelineEv[]; note?: string }) {
  return (
    <TitledCard id="timeline" titleId="tl-h" title={<>Timeline <span className="ncount">{list.length}</span></>}>
      {note ? <p className="card-sub">{note}</p> : null}
      {list.length ? <Events list={list.slice(0, OPEN)} /> : <p className="empty">No events.</p>}
      {list.length > OPEN ? <details className="fold"><summary>Earlier events <span className="muted">{list.length - OPEN}</span></summary><Events list={list.slice(OPEN)} /></details> : null}
    </TitledCard>
  );
}

function Csrf({ csrf, ...kv }: { csrf: string } & Record<string, string | number>) {
  return <><input type="hidden" name="csrf" value={csrf} />{Object.entries(kv).map(([k, v]) => <input key={k} type="hidden" name={k} value={String(v)} />)}</>;
}
export { Csrf as Hidden };

export function Item({ it }: { it: ProbItem }) {
  return (
    <>
      <span className="kind">{it.type}</span> {it.href ? <a href={it.href}>{it.label}</a> : it.label}{" "}
      {!it.exists ? <span className="muted">this item no longer exists</span> : <>{it.title ? <span dir="auto">{it.title}</span> : null} {it.status ? <span className="muted small">{it.status}</span> : null}</>}
    </>
  );
}

/** The summary that opens a confirm step: a danger word, the real button is inside. */
const DANGER_SUMMARY = buttonVariants({ variant: "destructive-ghost", size: "sm" });
/** A field beside its button in .inline-f. */
const GROW = "flex-[1_1_200px]";

export function Suggestion({ s, back, csrf }: { s: ProbSuggestion; back: string; csrf: string }) {
  return (
    <li id={`s-${s.id}`}>
      <p><b className="num">{s.confidence}</b> confidence: {s.item ? <Item it={s.item} /> : null}
        {s.problem ? <> may belong to <a href={`/problems/view?id=${s.problem.id}`}><b>{s.problem.label}</b></a> <span dir="auto">{s.problem.title}</span></> : null}</p>
      <p className="muted small">{s.linkWord} · from {s.from} · <When ts={s.at} /></p>
      {s.reason ? <p className="small" dir="auto">{s.reason}</p> : null}
      {s.canDecide ? (
        <div className="row-act">
          <form action={problemAct}><Csrf csrf={csrf} action="accept" link={s.id} back={back} /><Button variant="outline" size="sm">Accept link</Button></form>
          <details className="confirm"><summary data-slot="button" className={DANGER_SUMMARY}>Reject link…</summary>
            <form action={problemAct} className="inline-f"><Csrf csrf={csrf} action="reject" link={s.id} back={back} />
              <Field className={GROW} label="Reason"><Input name="note" maxLength={300} placeholder="Remembered for this pair" autoComplete="off" /></Field>
              <Button variant="destructive" size="sm">Reject link</Button></form></details>
        </div>
      ) : null}
    </li>
  );
}

/** Which problem this item follows, Argus's suggestions, and the controls to link or create one. */
export function ProblemBox({ p, csrf }: { p: ProblemPanel | null; csrf: string }) {
  if (!p) return null;
  const create = p.create ? (
    <details className="ctl"><summary>Create problem from this {p.create.noun}</summary>
      <form action={problemAct} className="ctl-f"><Csrf csrf={csrf} action="create" from={p.create.from} back={p.back} />
        <Field label="Title"><Input name="title" required minLength={3} maxLength={200} defaultValue={p.create.title} /></Field>
        <Field label="Note"><Input name="note" maxLength={300} placeholder="Optional" autoComplete="off" /></Field>
        <Button variant="outline" size="sm" className="justify-self-start">Create problem</Button>
        <p className="muted small">The new problem starts as investigating, with this {p.create.noun} linked to it.</p></form></details>
  ) : null;
  const l = p.link;
  const link = l ? (
    <details className="ctl" open={!!l.pq}><summary>{l.summary}</summary>
      <div className="ctl-f">
        <GetForm action={l.path + "#problem"} className="inline-f">
          {Object.entries(l.keep).map(([k, v]) => <input key={k} type="hidden" name={k} value={v} />)}
          <Label className={`grid ${GROW}`}><span className="sr">Find a problem</span><Input type="search" name="pq" defaultValue={l.pq} maxLength={80} placeholder="P-number or words of the title" autoComplete="off" /></Label>
          <Button variant="outline" size="sm">Find</Button></GetForm>
        {l.picks.length ? (
          <form action={problemAct} className="ctl-f"><Csrf csrf={csrf} action="link" back={p.back} /><input type="hidden" name="ref" value={p.ref} />
            <fieldset className="picks"><legend>{l.pq ? `Open problems matching ‘${l.pq}’` : "Newest open problems"}</legend>
              {l.picks.map((x) => <label key={x.id} className="check"><input type="radio" name="problem" value={x.id} required defaultChecked={l.picks.length === 1} /><span className="mono">P-{x.id}</span> <span dir="auto">{x.title}</span> <span className="muted small">{x.status}</span></label>)}
            </fieldset>
            <Field label="Link as"><Select className="w-full" name="link_type">{l.types.map((o) => <option key={o.value} value={o.value}>{o.label}</option>)}</Select></Field>
            <Field label="Note"><Input name="note" maxLength={300} placeholder="Why (optional)" autoComplete="off" /></Field>
            <Button variant="outline" size="sm" className="justify-self-start">{l.button}</Button>
            {l.button === "Suggest" ? <p className="muted small">An admin accepts or rejects your suggestion.</p> : null}
          </form>
        ) : <p className="muted small">{l.pq ? `No open problem matches ‘${l.pq}’.` : "No open problem yet. Create one from this page."}</p>}
      </div></details>
  ) : null;
  return (
    <TitledCard id="problem" titleId="pb-h" title="Problem" bodyClassName="pbox mt-2.5">
      <Banners list={p.banners} />
      {p.banner ? <p className="pb-line"><Icon name="info" size={16} /> <span><Segs s={p.banner} /></span></p> : null}
      {p.suggestions.length ? <><h3>Suggested by Argus <span className="ncount">{p.suggestions.length}</span></h3><ul className="sugs">{p.suggestions.map((s) => <Suggestion key={s.id} s={s} back={p.back} csrf={csrf} />)}</ul></> : null}
      {link || create ? <div className="ctls">{p.create?.first ? <>{create}{link}</> : <>{link}{create}</>}</div> : null}
    </TitledCard>
  );
}

export function LinkRow({ l, back, csrf }: { l: ProbLink; back: string; csrf: string }) {
  return (
    <li id={`l-${l.id}`}>
      <p><Item it={l.item} /></p>
      <p className="muted small">{l.secondary ? <><Pill title="Another problem is this item's primary">secondary</Pill> </> : null}{l.source} · {l.by} · <When ts={l.at} />{l.item.env ? <> · <EnvPill env={l.item.env} /></> : null}{l.state !== "accepted" ? <> · {l.state}</> : null}</p>
      {l.why ? <p className="small" dir="auto">{l.why}</p> : null}
      {l.canEdit ? (
        <div className="row-act">
          {l.secondary ? <form action={problemAct}><Csrf csrf={csrf} action="primary" link={l.id} back={back} /><Button variant="outline" size="sm">Make primary</Button></form> : null}
          <details className="confirm"><summary data-slot="button" className={DANGER_SUMMARY}>Unlink…</summary>
            <form action={problemAct} className="inline-f"><Csrf csrf={csrf} action="unlink" link={l.id} back={back} />
              <Field className={GROW} label="Reason"><Input name="note" required minLength={3} maxLength={300} placeholder="Why it is not part of this problem" autoComplete="off" /></Field>
              <Button variant="destructive" size="sm">Unlink</Button></form></details>
        </div>
      ) : null}
    </li>
  );
}

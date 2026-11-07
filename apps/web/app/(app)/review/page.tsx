import { HelpQ } from "@/components/HelpQ";
import type { Metadata } from "next";
import { redirect } from "next/navigation";
import { Banner } from "@/components/Banner";
import { Icon } from "@/components/Icon";
import { EnvPill, KindTag, Pill } from "@/components/Pills";
import { SevMark, SevPill } from "@/components/Severity";
import { Button, buttonVariants } from "@/components/ui/button";
import { Card } from "@/components/ui/card";
import { Field, Input } from "@/components/ui/field";
import { qs } from "@/lib/api";
import { day, full, iso, minutes, plural } from "@/lib/format";
import { apiGet, csrfToken, readFlash } from "@/lib/server";
import type { Action, Fact, Review, ReviewDetail, ReviewRow, Section } from "@/lib/types";
import { reviewAct } from "./actions";
import { IdeasFull } from "./Ideas";
import { LessonsFull } from "./Lessons";
import { LinksFull } from "./Links";
import { ProposalsFull } from "./Proposals";
import { RepliesFull } from "./Replies";
import { anchorOf, href, queueOf, type Q } from "./query";
import { cn } from "cn";

export const metadata: Metadata = { title: "Review inbox" };

type Search = Promise<Record<string, string | string[] | undefined>>;

const PASS = ["kind", "q", "sev", "env", "src", "sort", "page", "sel", "step", "mode", "req", "sent", "preq", "view", "confirm",
  "status", "persona", "area", "effort", "out", "p", "review", "all", "conf", "type", "lkind"];

function one(v: string | string[] | undefined): string {
  return (Array.isArray(v) ? v[0] : v) ?? "";
}

function Row({ it, q, selected }: { it: ReviewRow; q: Q; selected: boolean }) {
  const now = Date.now() / 1000;
  const soon = it.due ? it.due - now < 86400 : false;
  return (
    <li id={anchorOf(it.sel)} className={selected ? "q-row on" : "q-row"}>
      <a data-slot="review-row" className="rv-link" href={href({ ...q, sel: it.sel, step: "" }) + "#" + anchorOf(it.sel)} aria-current={selected ? "true" : undefined}>
        {it.sevKey === "none" ? <span aria-hidden="true" /> : <SevMark sev={it.sevKey} />}
        <span className="rv-main">
          <span className="rv-title" title={it.title} dir="auto">{it.title}</span>
          <span className="rv-meta">
            {it.kindRow}
            {it.env ? <> · {it.prod ? <b className="font-semibold text-foreground">prod</b> : it.envLabel}</> : null}
            {it.sourceLabel ? <> · from {it.sourceLabel}</> : null}
            {it.targets.length ? <> · <span className="mono">{it.targets[0]}{it.targets.length > 1 ? ` +${it.targets.length - 1}` : ""}</span></> : null}
          </span>
        </span>
        <span className="rv-end">
          {it.due ? (
            soon ? <Pill tone="warn" title={`Decision deadline ${full(it.due)}`}>{it.dueWord?.replace("proposal ", "").replace("request ", "")} {it.dueLeft}</Pill>
              : <span className="muted" title={`Decision deadline ${full(it.due)}`}>expires {day(it.due)}</span>
          ) : null}
          <time className="num muted" dateTime={iso(it.at)} title={`Waiting since ${full(it.at)}`}>{it.waiting.replace("waiting ", "")}</time>
        </span>
        {selected ? <span className="sr">, in the panel</span> : null}
      </a>
    </li>
  );
}

function Facts({ facts, one }: { facts: Fact[]; one?: boolean }) {
  if (!facts.length) return null;
  return (
    <dl className={one ? "rv-facts rv-facts-one" : "rv-facts"}>
      {facts.map((f, i) => (
        <div key={i}>
          <dt>{f.label}</dt>
          <dd className={f.missing ? "rv-missing" : f.mono ? "mono" : undefined}>{f.href && !f.missing ? <a href={f.href}>{f.value}</a> : f.value}</dd>
        </div>
      ))}
    </dl>
  );
}

const H3 = "flex items-center gap-1.5 text-base font-semibold";
const LIST = "grid list-disc gap-1.5 pl-[18px] text-sm";
const TINT = { info: "rounded-[var(--r-inner)] bg-info px-3.5 py-3", warn: "rounded-[var(--r-inner)] bg-warn px-3.5 py-3" };
/** The tall buttons of the panel: the decision is the point of the page. */
const TALL = "h-(--control-lg)";

function Sec({ id, title, s, callout }: { id: string; title: string; s: Section; callout?: boolean }) {
  if (!s.text && !s.items?.length && !s.facts?.length && !s.code) return null;
  const ink = callout ? "text-info-foreground" : s.missing ? "text-warn-foreground" : "text-ink-2";
  const body = (
    <>
      {s.facts?.length ? <Facts facts={s.facts} one /> : null}
      {s.text ? <p className={cn("wrap-anywhere whitespace-pre-line", ink)} dir="auto">{s.text}</p> : null}
      {s.items?.length ? <ul className={cn(LIST, callout ? ink : "text-ink-2")}>{s.items.map((x, i) => <li key={i}>{x.href ? <a href={x.href}>{x.text}</a> : x.text}</li>)}</ul> : null}
      {s.code ? (
        s.codeLabel ? <details className="fold"><summary>{s.codeLabel}</summary><pre className="rv-code"><code>{s.code}</code></pre></details>
          : <pre className="rv-code"><code>{s.code}</code></pre>
      ) : null}
    </>
  );
  return (
    <section className={cn("grid gap-2", callout ? TINT.info : s.missing ? TINT.warn : "")} aria-labelledby={`ps-${id}`}>
      <h3 className={cn(H3, (callout || s.missing) && ink)} id={`ps-${id}`}>{s.missing ? <><Icon name="warn" /> {title}</> : title}</h3>
      {body}
    </section>
  );
}

function Act({ a, csrf, sel, ret, here, planVersion }: { a: Action; csrf: string; sel: string; ret: string; here: string; planVersion?: number }) {
  const variant = a.style === "primary" ? "default" : "outline";
  if (a.href) return <a data-slot="button" className={cn(buttonVariants({ variant }), TALL)} href={a.href}>{a.label}</a>;
  const hidden = (
    <>
      <input type="hidden" name="csrf" value={csrf} />
      <input type="hidden" name="sel" value={sel} />
      <input type="hidden" name="action" value={a.action} />
      <input type="hidden" name="return" value={ret} />
      <input type="hidden" name="here" value={here} />
      {planVersion !== undefined ? <input type="hidden" name="plan_version" value={planVersion} /> : null}
    </>
  );
  const disabled = !!a.disabled;
  if (a.confirm) {
    return (
      <details className="open:basis-full">
        <summary data-slot="button" className={cn(buttonVariants({ variant: "destructive-ghost" }), TALL, "list-none [&::-webkit-details-marker]:hidden")}>{a.label.split(" ")[0]}…</summary>
        <form action={reviewAct} className="mt-2 flex flex-wrap items-end gap-2">
          {hidden}
          {a.reason ? (
            <Field className="min-w-[200px] flex-1" label={a.reasonRequired ? "Reason" : "Reason (optional)"}>
              <Input name="reason" maxLength={300} minLength={a.reasonRequired ? a.reasonMin : undefined} required={a.reasonRequired}
                placeholder={a.reason} autoComplete="off" />
            </Field>
          ) : null}
          <Button variant="destructive" className={TALL}>{a.label}</Button>
        </form>
      </details>
    );
  }
  const id = `why-${a.action}`;
  return (
    <form className="grid gap-1" action={reviewAct}>
      {hidden}
      <Button variant={variant} className={TALL} disabled={disabled} aria-describedby={disabled ? id : undefined}>{a.label}</Button>
      {disabled ? <span id={id} className="max-w-[32ch] text-xs text-muted-foreground">{a.disabled}</span> : null}
    </form>
  );
}

const STEPS = ["Proposed", "Plan reviewed", "Approved", "Running", "Done"];

function Stepper({ current }: { current: number }) {
  return (
    <ol className="grid grid-cols-5" aria-label="Approval progress">
      {STEPS.map((s, i) => (
        <li key={s} className={cn("relative grid justify-items-center gap-1.5 text-center not-first:before:absolute not-first:before:top-[11px] not-first:before:-left-1/2 not-first:before:h-0.5 not-first:before:w-full", i <= current ? "not-first:before:bg-link" : "not-first:before:bg-border")} aria-current={i === current ? "step" : undefined}>
          <span className={cn("relative z-1 grid size-6 place-items-center rounded-full", i < current ? "bg-primary text-primary-foreground" : i === current ? "bg-card shadow-[inset_0_0_0_2px_var(--accent)] after:size-2 after:rounded-full after:bg-link" : "bg-card shadow-[inset_0_0_0_1.5px_var(--control-border)]")} aria-hidden="true">{i < current ? <Icon name="check" size={12} /> : null}</span>
          <span className={i === current ? "text-sm font-semibold text-foreground" : "text-sm font-medium text-muted-foreground"}><span className="sr">{i < current ? "done: " : i === current ? "current: " : "to do: "}</span>{s}</span>
        </li>
      ))}
    </ol>
  );
}

/** The panel: beside the queue on a wide screen, in place of it on a narrow one. */
const PANEL = "sticky top-[calc(var(--topbar)+16px)] grid max-h-[calc(100vh-var(--topbar)-32px)] gap-5 overflow-y-auto px-(--card-spacing) max-[1199px]:static max-[1199px]:max-h-none max-[1199px]:overflow-visible";

function Back({ href }: { href: string }) {
  return <a className="inline-flex items-center gap-1.5 justify-self-start text-sm font-medium" href={href}><Icon name="back" /> <span className="min-[1200px]:hidden">Back to queue</span><span className="hidden min-[1200px]:inline">Close</span></a>;
}

function Panel({ d, q, csrf }: { d: ReviewDetail; q: Q; csrf: string }) {
  const it = d.row;
  const ret = href(queueOf(q));
  const here = href(q);
  const plan = d.plan;
  const planStep = it.kind === "approvals" && d.step === "plan" && plan;
  const acts = (
    <div className="sticky -bottom-(--card-spacing) -mx-(--card-spacing) grid gap-2 border-t bg-card px-(--card-spacing) py-4 max-[1199px]:bottom-0">
      <div className="flex flex-wrap items-start gap-2">
        {d.actions.map((a, i) => <Act key={i} a={a} csrf={csrf} sel={it.sel} ret={ret} here={here} planVersion={a.action === "approve" ? plan?.version : undefined} />)}
      </div>
      {d.readOnly ? <p className="muted">Admins decide this; you can read it.</p> : d.note ? <p className="text-sm text-muted-foreground">{d.note}</p> : null}
    </div>
  );
  return (
    <Card as="section" className={PANEL} id="rv-panel" aria-labelledby="p-title">
      <Back href={ret + "#" + anchorOf(it.sel)} />
      <header className="grid gap-2">
        <div className="flex flex-wrap items-center gap-1.5">
          <KindTag>{it.kindRow}</KindTag>
          {it.sevKey !== "none" ? <SevPill sev={it.sevKey} word={it.severity} /> : null}
          {it.env ? <EnvPill env={it.env} label={it.envLabel} /> : null}
        </div>
        <h2 className="text-xl font-semibold text-pretty wrap-anywhere" id="p-title" dir="auto">{it.title}</h2>
        {it.summary ? <p className="text-ink-2" dir="auto">{it.summary}</p> : null}
      </header>
      {it.kind === "approvals" ? <Stepper current={planStep ? 1 : 0} /> : null}
      {planStep ? (
        <>
          <h3 className={H3}>Review plan v{plan.version} before approving</h3>
          <dl className="rv-facts">
            <div><dt>Plan version</dt><dd><b>v{plan.version}</b> <span className="muted">approving sends exactly this version</span></dd></div>
            <div><dt>Environment</dt><dd className={it.env ? undefined : "rv-missing"}>{it.env ? (it.prod ? "production (release)" : it.envLabel) : "Not set in the plan"}</dd></div>
            <div><dt>Affected targets</dt><dd className={it.targets.length ? "mono" : "rv-missing"}>{it.targets.length ? it.targets.join(", ") : "No targets in the plan"}</dd></div>
            <div><dt>Estimate</dt><dd className={plan.estimate ? undefined : "rv-missing"}>{plan.estimate || "Not in the plan"}</dd></div>
          </dl>
          <section className="grid gap-2" aria-labelledby="ps-chg">
            <h3 className={H3} id="ps-chg">Intended changes <span className="muted">· {plural(plan.steps.length, "step")}, {plan.writes} WRITE</span></h3>
            {plan.steps.length ? (
              <ol className="rv-steps">
                {plan.steps.map((s, i) => (
                  <li key={i}>
                    <span dir="auto">{s.text}</span>
                    <span className="flex flex-wrap items-center gap-1.5"><span className={s.mode === "WRITE" ? "pill warn" : "pill neutral"}>{s.mode}</span>
                      {s.targets.map((x) => <span key={x} className="mono small">{x}</span>)}</span>
                  </li>
                ))}
              </ol>
            ) : <p className="rv-missing">The plan lists no steps.</p>}
          </section>
          <section className="grid gap-2" aria-labelledby="ps-val"><h3 className={H3} id="ps-val">Validation and rollback</h3>
            <p><span className="rv-missing">Not recorded in plan v{plan.version}.</span> <span className="muted">The plan format has no validation or rollback field; read the steps above and ask Argus on the task if you need it.</span></p></section>
          <section className={cn("grid gap-2", TINT.info)} aria-labelledby="ps-lim"><h3 className={cn(H3, "text-info-foreground")} id="ps-lim">Limits</h3>
            <ul className={cn(LIST, "text-info-foreground")}>{plan.limits.map((l) => <li key={l}>{l}</li>)}</ul></section>
          {acts}
        </>
      ) : (
        <>
          <Facts facts={d.facts} />
          <Sec id="out" title="Proposed outcome" s={d.outcome} />
          <Sec id="ev" title={d.evidence.missing ? "No evidence recorded" : "Evidence"} s={d.evidence} />
          <Sec id="after" title={d.afterTitle} s={d.after} callout />
          {acts}
          {d.more.length ? (
            <details className="fold [&_dl]:mt-2.5"><summary className="text-muted-foreground!">Details and history</summary><Facts facts={d.more} one /></details>
          ) : null}
        </>
      )}
    </Card>
  );
}

export default async function ReviewPage({ searchParams }: { searchParams: Search }) {
  const sp = await searchParams;
  const params: Record<string, string> = {};
  for (const k of PASS) if (one(sp[k])) params[k] = one(sp[k]);
  if (params.mode === "full" && params.kind === "ideas") return <IdeasFull params={params} />;
  if (params.mode === "full" && params.kind === "proposals") {
    const all = new URLSearchParams();
    for (const k of [...PASS, "id"]) for (const v of [sp[k]].flat()) if (v) all.append(k, v);
    return <ProposalsFull query={all.toString()} />;
  }
  if (params.mode === "full" && params.kind === "lessons") return <LessonsFull params={params} />;
  if (params.mode === "full" && params.kind === "replies") return <RepliesFull params={params} />;
  if (params.mode === "full" && params.kind === "links") {
    const all = new URLSearchParams();
    for (const k of [...PASS, "link"]) for (const v of [sp[k]].flat()) if (v) all.append(k, v);
    return <LinksFull query={all.toString()} />;
  }
  const [r, csrf, flash, routes] = await Promise.all([apiGet<Review>("/api/v1/review" + qs(params)), csrfToken(), readFlash(), apiGet<{ total: number }>("/api/v1/sources/review")]);
  if (r.status !== 200) throw new Error(`/api/v1/review answered ${r.status}`);
  const d = r.data;
  const q = d.query;
  if (d.fullMode && params.mode !== "full") redirect("/review" + qs({ ...params, mode: "full" }));
  const kindLabel = d.kinds.find((k) => k.key === q.kind);
  const noun = kindLabel ? kindLabel.row.toLowerCase() : "decision";
  const active = d.filtered || q.sort !== "urgency";
  const from = (d.page - 1) * d.pageSize;
  // narrow: the panel replaces the queue while a decision is selected. Important: .tabs and .filters are unlayered.
  const away = q.sel ? "max-[1199px]:hidden!" : undefined;

  return (
    <div>
      {d.refresh ? <meta httpEquiv="refresh" content="3" /> : null}
      <header className="phead">
        <div>
          <h1>Review inbox</h1><HelpQ p="/review" />
          <p className="psub">Which decisions are waiting for you, most urgent first?</p>
          <p className="status-line">
            <b>{d.total === 0 ? "Nothing waiting" : `${plural(d.total, "decision")} wait${d.total === 1 ? "s" : ""}`}</b>
            {d.total > 0 && d.admin ? <><span className="sep">·</span>about {minutes(d.estMin)}</> : null}
            {!d.admin ? <><span className="sep">·</span>read-only: admins decide</> : null}
            {routes.status === 200 && routes.data.total > 0 ? <><span className="sep">·</span><a href="/admin/sources/catalogue#decisions">{plural(routes.data.total, "routing decision")}</a></> : null}
          </p>
        </div>
        {q.kind && q.kind !== "approvals" && d.admin ? <a data-slot="button" className={buttonVariants({ variant: "ghost" })} href={`/review?kind=${q.kind}&mode=full`}>Open full list</a> : null}
      </header>

      {flash ? <Banner b={flash} /> : null}
      {d.banners.map((b, i) => <Banner key={i} b={b} />)}

      <nav className={cn("tabs", away)} aria-label="Decision kinds">
        {[{ key: "", label: "All", count: d.total }, ...d.kinds].map((k) => {
          const on = k.key === q.kind;
          return (
            <a key={k.key || "all"} href={href({ ...queueOf(q), kind: k.key, page: 1 })} className={on ? "on" : undefined} aria-current={on ? "page" : undefined}>
              {k.label} <span className="num">{k.count}</span>
            </a>
          );
        })}
      </nav>

      {d.kindTotal > 0 ? (
        <form className={cn("filters", away)} method="get" action="/review" role="search" aria-label="Search and filter decisions">
          {q.kind ? <input type="hidden" name="kind" value={q.kind} /> : null}
          <label className="f-search">
            <Icon name="search" />
            <span className="sr">Search</span>
            <input name="q" defaultValue={q.q} maxLength={100} placeholder="Title, host, service, ID" autoComplete="off" />
          </label>
          {([["sev", "Severity", d.options.sev, "Any"], ["env", "Environment", d.options.env, "Any"], ["src", "Source", d.options.src, "Any"],
            ["sort", "Sort", d.options.sort, ""]] as const).map(([name, label, opts, any]) => {
            const val = q[name];
            const on = name === "sort" ? val !== "urgency" : !!val;
            return (
              <label key={name} className={on ? "f-pill on" : "f-pill"} title={name === "src" ? "Source applies to task proposals: the intake path that proposed them" : undefined}>
                <span className="f-l">{label}</span>
                <select name={name} defaultValue={val}>
                  {any ? <option value="">{any}</option> : null}
                  {opts.map((o) => <option key={o.value} value={o.value}>{o.label}</option>)}
                </select>
              </label>
            );
          })}
          <Button variant="outline">Apply</Button>
          {active ? <a data-slot="button" className={buttonVariants({ variant: "ghost" })} href={href({ kind: q.kind })}><Icon name="reset" />Reset</a> : <Button type="button" variant="ghost" disabled><Icon name="reset" />Reset</Button>}
          <span className="f-meta num">{d.filtered ? `${d.matched} of ${d.kindTotal}` : `${d.matched}`} {d.admin ? <>waiting · ~{minutes(d.estMin)}</> : "waiting"}</span>
        </form>
      ) : null}
      {d.kindTotal > 0 ? <p className={cn("mt-2.5 mb-4 text-sm text-muted-foreground", away)}>{d.order}</p> : null}

      <div className={cn("grid items-start gap-4 max-[1199px]:grid-cols-[minmax(0,1fr)]", q.sel ? "grid-cols-[minmax(0,7fr)_minmax(0,5fr)]" : "grid-cols-[minmax(0,1fr)]")}>
        <Card as="section" className={cn("block p-2", away)} aria-labelledby="q-h">
          <h2 id="q-h" className="sr">Queue</h2>
          {d.kindTotal === 0 && !q.kind ? (
            <p className="empty p-4">No decisions waiting. Every decision Argus needs from a person lands here: proposals, plans, lessons, ideas, chat requests and problem links. Nothing reaches a tracker, a chat or a host until someone accepts it.</p>
          ) : d.kindTotal === 0 ? (
            <p className="empty p-4">No {noun} waits. <a href="/review">All decisions</a></p>
          ) : d.matched === 0 ? (
            <p className="empty p-4">No {noun} matches these filters. <a href={href({ kind: q.kind, sort: q.sort })}>Reset the filters</a></p>
          ) : (
            <>
              <ul className="q-list">
                {d.items.map((it) => <Row key={it.sel} it={it} q={q} selected={it.sel === q.sel} />)}
              </ul>
              <footer className="flex items-center justify-between gap-3 px-3 pt-3 pb-1 text-sm">
                <span className="num muted">{from + 1}–{from + d.items.length} of {d.matched}{d.capped ? ` (the first ${d.perKind} of each kind)` : ""}</span>
                <span className="flex gap-2">
                  {d.page > 1 ? <a data-slot="button" className={buttonVariants({ variant: "outline", size: "sm" })} href={href({ ...q, page: d.page - 1 })} rel="prev">Prev</a> : null}
                  {d.page < d.pages ? <a data-slot="button" className={buttonVariants({ variant: "outline", size: "sm" })} href={href({ ...q, page: d.page + 1 })} rel="next">Next</a> : null}
                </span>
              </footer>
            </>
          )}
        </Card>

        {d.selected ? <Panel d={d.selected} q={q} csrf={csrf} /> : q.sel && d.selGone ? (
          <Card as="section" className={PANEL} aria-label="Review panel">
            <Back href={href(queueOf(q))} />
            <p className="empty"><b>This decision is no longer waiting.</b> It was decided, expired or withdrawn since this link was made, or you may not see it. Nothing was changed.</p>
          </Card>
        ) : null}
      </div>
    </div>
  );
}

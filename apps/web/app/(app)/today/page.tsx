import type { Metadata } from "next";
import { Banner } from "@/components/Banner";
import { Eye, Icon } from "@/components/Icon";
import { EnvPill, Pill } from "@/components/Pills";
import { SevMark } from "@/components/Severity";
import { Button } from "@/components/ui/button";
import { Card, CardAction, CardContent, CardDescription, CardFooter, CardHeader, CardTitle } from "@/components/ui/card";
import { Field, Input, Select } from "@/components/ui/field";
import { hours, minutes, plural } from "@/lib/format";
import { apiGet, csrfToken, readFlash } from "@/lib/server";
import { ago, full, iso } from "@/lib/format";
import type { Today, TodayChecks, TodayIncidents, TodayItem } from "@/lib/types";
import { cn } from "cn";
import { todayAct } from "./actions";

export const metadata: Metadata = { title: "Today" };

type Search = Promise<{ sent?: string; at?: string }>;

function Hidden({ csrf, ...kv }: { csrf: string } & Record<string, string | number>) {
  return (
    <>
      <input type="hidden" name="csrf" value={csrf} />
      {Object.entries(kv).map(([k, v]) => <input key={k} type="hidden" name={k} value={String(v)} />)}
    </>
  );
}

/** The status a finding states: a production change that needs a window or is under way, else informational. */
function tone(it: TodayItem): "fail" | "warn" | "info" {
  if (it.envShort === "prod" && (it.area === "incidents" || it.area === "backups")) return "fail";
  if (it.status === "doing" || it.needsWindow || it.carried > 0) return "warn";
  return "info";
}

const CIRCLE = { fail: "bg-fail text-fail-foreground", warn: "bg-warn text-warn-foreground", info: "bg-info text-info-foreground", ok: "bg-ok text-ok-foreground" };

function Circle({ tone, hidden, children }: { tone: keyof typeof CIRCLE; hidden?: boolean; children: React.ReactNode }) {
  return <span className={cn("grid size-8 shrink-0 place-items-center rounded-full text-sm font-semibold tabular-nums", CIRCLE[tone])} aria-hidden={hidden}>{children}</span>;
}

/** A chart legend entry: the swatch, the words, the value. */
function Legend({ swatch, children }: { swatch?: string; children: React.ReactNode }) {
  return <span className="inline-flex items-center gap-1.5 [&_b]:font-medium [&_b]:text-foreground [&_b]:tabular-nums">{swatch ? <span className={swatch} aria-hidden="true" /> : null}{children}</span>;
}

/** The link at the foot of a card. */
function FootLink({ href, children }: { href: string; children: React.ReactNode }) {
  return <p className="m-0"><a className="inline-flex items-center gap-1 font-medium" href={href}>{children} <Icon name="arrow" size={14} /></a></p>;
}

function Flags({ it }: { it: TodayItem }) {
  return (
    <>
      {it.status === "doing" ? <Pill tone="warn">doing</Pill> : null}
      {it.envShort ? <EnvPill env={it.env} short={it.envShort} /> : null}
      {it.needsWindow ? <Pill tone="neutral" title="A production change: plan a maintenance window">needs window</Pill> : null}
      {it.needsApproval ? <Pill tone="neutral" title="The operator approves it first">needs approval</Pill> : null}
      {it.carried > 0 ? <Pill tone="neutral" title="Not done on earlier days; re-scored">carried over{it.carried > 1 ? ` ${it.carried} d` : ""}</Pill> : null}
    </>
  );
}

/** Check runs per hour, not OK stacked on OK, the worst hour named under the chart. */
function ChecksCard({ c }: { c: TodayChecks }) {
  const top = Math.max(1, ...c.hours.map((h) => h.ok + h.fail));
  const worst = c.worst >= 0 ? c.hours[c.worst] : undefined;
  return (
    <Card as="section" className="gap-0" aria-labelledby="ck-h">
      <CardHeader>
        <CardTitle><h2 id="ck-h">Checks, last 48 h</h2></CardTitle>
        <CardDescription>Scheduled check runs per hour · all environments</CardDescription>
      </CardHeader>
      <CardContent>
      {c.total === 0 ? <><svg className="mt-3 block h-2 w-full" viewBox="0 0 480 8" preserveAspectRatio="none" aria-hidden="true"><line className="stroke-muted-foreground" x1={0} x2={480} y1={7.5} y2={7.5} vectorEffect="non-scaling-stroke" /></svg><p className="empty">No runs in the last 48 h.</p></> : (
        <figure className="mt-3">
          <svg className="block h-24 w-full stroke-card" viewBox="0 0 480 80" preserveAspectRatio="none" role="img" aria-label={`${c.total} check runs in 48 hours, ${c.failed} not OK`}>
            {c.hours.map((h, i) => {
              const hOk = (h.ok / top) * 78;
              const hFail = (h.fail / top) * 78;
              return (
                <g key={h.at}>
                  <title>{`${full(h.at)}: ${h.ok} OK, ${h.fail} not OK`}</title>
                  {h.ok ? <rect className="fill-data-green-soft" x={i * 10 + 1} y={80 - hOk} width={8} height={hOk} /> : null}
                  {h.fail ? <rect className="fill-data-red" x={i * 10 + 1} y={80 - hOk - hFail} width={8} height={hFail} /> : null}
                </g>
              );
            })}
            <line className="stroke-muted-foreground" x1={0} x2={480} y1={79.5} y2={79.5} vectorEffect="non-scaling-stroke" />
          </svg>
          <p className="num mt-1 flex justify-between text-xs text-muted-foreground"><span>{full(c.hours[0]!.at)}</span><span>{full(c.hours[c.hours.length - 1]!.at)}</span></p>
          <figcaption className="mt-2 flex flex-wrap gap-x-4 gap-y-1 text-sm text-muted-foreground">
            <Legend swatch="size-2.5 rounded-[3px] bg-data-green-soft">OK <b>{c.total - c.failed}</b></Legend>
            <Legend swatch="size-2.5 rounded-[3px] bg-data-red">Not OK <b>{c.failed}</b></Legend>
            {worst ? <Legend>Worst hour <b>{full(worst.at)}</b>, {worst.fail} not OK</Legend> : null}
          </figcaption>
          <details className="fold"><summary>Show data</summary>
            <div className="overflow-x-auto" role="region" aria-label="Check runs per hour" tabIndex={0}><table className="mt-2 w-full text-sm [&_td]:p-px [&_th]:p-px [&_th]:text-left [&_th]:font-medium [&_th]:text-muted-foreground">
              <thead><tr><th scope="col">Hour</th><th scope="col">OK</th><th scope="col">Not OK</th></tr></thead>
              <tbody>{c.hours.filter((h) => h.ok + h.fail > 0).map((h) => <tr key={h.at}><td>{full(h.at)}</td><td className="num">{h.ok}</td><td className="num">{h.fail}</td></tr>)}</tbody>
            </table></div>
          </details>
        </figure>
      )}
      </CardContent>
      <CardFooter className="mt-5 text-sm"><FootLink href="/operations">View checks</FootLink></CardFooter>
    </Card>
  );
}

/** The top incident with its stage and time in it, the others as rows. */
function IncidentsCard({ d }: { d: TodayIncidents }) {
  const t = d.top;
  const stage = t?.status === "acknowledged" ? 1 : 0;
  return (
    <Card as="section" className="gap-0" aria-labelledby="in-h">
      <CardHeader>
        <CardTitle><h2 id="in-h">Active incidents</h2></CardTitle>
        {d.total ? <CardAction className="row-span-1"><Pill tone={d.open ? "fail" : "neutral"}>{d.total} active</Pill></CardAction> : null}
      </CardHeader>
      <CardContent>
      {!t ? <p className="empty">No open incident. New findings open one; the list fills itself.</p> : (
        <>
          <a className="mt-2.5 flex items-center gap-2.5 font-semibold text-foreground" href={`/incidents/view?id=${t.id}`}><SevMark sev={t.sevKey} /><span dir="auto">{t.title}</span></a>
          <ol className="mt-3 mb-2 ml-3 grid gap-2 text-sm" aria-label="Incident stage">
            {["Opened", "Acknowledged", "Resolved"].map((w, i) => (
              <li key={w} className={cn("flex flex-wrap items-center gap-2", i > stage && "text-muted-foreground")} aria-current={i === stage ? "step" : undefined}>
                <span className={cn("size-2.5 rounded-full border-2", i < stage ? "border-link bg-link" : i === stage ? "border-link bg-card" : "border-border bg-card")} aria-hidden="true" />
                <span>{w}{i === 0 ? <time className="muted" dateTime={iso(t.firstSeen)} title={full(t.firstSeen)}> {ago(t.firstSeen)}</time> : null}{i === 1 && t.ackedAt ? <span className="muted"> by {t.ackedBy}</span> : null}</span>
                {i === stage ? <Pill tone="neutral" title="Time in this stage">{t.stageFor}</Pill> : null}
              </li>
            ))}
          </ol>
          {d.rows.length ? (
            <ul className="mt-3">
              {d.rows.map((x) => (
                <li key={x.id}><a data-slot="mini-link" className="mini-q" href={`/incidents/view?id=${x.id}`}><SevMark sev={x.sevKey} /><span className="truncate text-sm font-semibold">{x.title}</span><span className="text-xs text-muted-foreground">{x.status}{x.env === "release" ? " · prod" : x.env ? ` · ${x.env}` : ""} · <time dateTime={iso(x.last)} title={full(x.last)}>{ago(x.last)}</time></span></a></li>
              ))}
            </ul>
          ) : null}
        </>
      )}
      </CardContent>
      <CardFooter className="mt-5 text-sm"><FootLink href="/incidents">View incidents</FootLink></CardFooter>
    </Card>
  );
}

/** The day as one bar: done, planned, decisions waiting, free time; the tick is the focus budget. */
function DayBar({ done, planned, review, budget }: { done: number; planned: number; review: number; budget: number }) {
  const rest = Math.max(0, planned - done);
  const free = Math.max(0, budget - planned - review);
  const total = Math.max(budget, planned + review, 1);
  const segs = [
    { k: "done", v: done, label: "Done", fill: "fill-data-1", sw: "bg-data-1" },
    { k: "planned", v: rest, label: "Planned, still to do", fill: "fill-data-1-soft", sw: "bg-data-1-soft" },
    { k: "review", v: review, label: "Decisions waiting", fill: "fill-data-sand", sw: "bg-data-sand" },
    { k: "free", v: free, label: "Free time", fill: "fill-data-grey", sw: "bg-data-grey" },
  ];
  let x = 0;
  return (
    <Card as="figure" className="block px-(--card-spacing)">
      <svg className="block h-3 w-full overflow-hidden rounded-[6px] bg-data-grey" viewBox="0 0 1000 12" preserveAspectRatio="none" role="img" aria-label={segs.map((s) => `${s.label} ${minutes(s.v)}`).join(", ") + `; focus budget ${minutes(budget)}`}>
        {segs.map((s) => {
          const w = (s.v / total) * 1000;
          const r = (
            <g key={s.k}>
              <rect className={s.fill} x={x} y={0} width={Math.max(0, w)} height={12} />
              {x > 0 && w > 0 ? <line className="stroke-card stroke-2" x1={x} x2={x} y1={0} y2={12} vectorEffect="non-scaling-stroke" /> : null}
            </g>
          );
          x += w;
          return r;
        })}
        <line className="stroke-data-target stroke-2" x1={Math.min(999, (budget / total) * 1000)} x2={Math.min(999, (budget / total) * 1000)} y1={0} y2={12} vectorEffect="non-scaling-stroke" />
      </svg>
      <figcaption className="mt-2.5 flex flex-wrap gap-x-[18px] gap-y-1.5 text-sm text-muted-foreground">
        {segs.map((s) => (
          <Legend key={s.k} swatch={`size-2.5 rounded-[3px] ${s.sw}`}>{s.label} <b>{minutes(s.v)}</b></Legend>
        ))}
        <Legend swatch="h-3 w-0.5 bg-data-target">Your focus time <b>{hours(budget)}</b></Legend>
      </figcaption>
    </Card>
  );
}

export default async function TodayPage({ searchParams }: { searchParams: Search }) {
  const sp = await searchParams;
  const q = new URLSearchParams();
  if (sp.sent) q.set("sent", sp.sent);
  if (sp.at) q.set("at", sp.at);
  const [r, csrf, flash] = await Promise.all([apiGet<Today>("/api/v1/today" + (q.size ? "?" + q : "")), csrfToken(), readFlash()]);
  if (r.status !== 200) throw new Error(`/api/v1/today answered ${r.status}`);
  const t = r.data;
  const admin = !!t.admin;
  const act = !!t.canAct;
  const p = t.plan;
  const open = t.open ?? [];
  const done = t.done ?? [];
  const later = t.later ?? [];
  const nPlan = open.length + done.length;
  const rv = t.review;

  const status = p ? (
    <p className="status-line">
      <b>Planned {nPlan} · ~{hours(p.plannedMin)}</b> of {admin ? "your" : "the operator's"} {hours(p.budgetMin)}
      <span className="sep">·</span>{done.length} done<span className="sep">·</span>{p.dayLabel}{p.isToday ? "" : " (not today's plan yet)"}
      {admin ? null : <><span className="sep">·</span>read-only: admins work this plan</>}
    </p>
  ) : null;

  return (
    <>
      {t.banner?.refresh ? <meta httpEquiv="refresh" content={String(t.banner.refresh)} /> : null}
      <header className="phead">
        <div>
          <h1>Today</h1>
          <p className="psub">What needs you now, and in what order?</p>
          {status}
        </div>
        {act ? (
          <form action={todayAct} className="phead-act" data-cmd="replan">
            <Hidden csrf={csrf} action="replan" />
            <Button variant="outline" title="Collect everything again and refill the day"><Icon name="reset" />Re-plan</Button>
          </form>
        ) : null}
      </header>
      {flash ? <Banner b={flash} /> : null}
      {t.banner ? <Banner b={t.banner} /> : null}

      {!t.on ? (
        <Card as="section"><CardContent><p className="empty">The daily planner is off on this installation. <a href="/operations">Checks</a> and the <a href="/review">Review inbox</a> still show what needs a person.</p></CardContent></Card>
      ) : !p ? (
        <Card as="section"><CardContent><p className="empty">No plan yet. Argus plans the day at {t.planAt}{act ? "; Re-plan builds one now." : "."}</p></CardContent></Card>
      ) : (
        <>
          <DayBar done={p.doneMin} planned={p.plannedMin} review={admin ? rv.estMin : 0} budget={p.budgetMin} />

          <div className="grid grid-cols-[minmax(0,1.7fr)_minmax(320px,1fr)] items-start gap-4 max-[1180px]:grid-cols-[minmax(0,1fr)]">
          <div className="grid min-w-0 gap-4">
          <Card as="section" className="gap-0" aria-labelledby="needs-h">
            <CardHeader>
              <CardTitle><h2 id="needs-h" className="flex items-center gap-2 [&_.eye]:text-link"><Eye /> {admin ? "Needs you" : "Top of today's plan"} <span className="muted text-base font-normal">· {admin ? "in today's plan order" : "read-only, admins work it"}</span></h2></CardTitle>
              <CardAction className="row-span-1"><Pill tone="neutral">{plural(open.length, "item")}</Pill></CardAction>
            </CardHeader>
            <CardContent>
            {open.length === 0 ? (
              <p className="mt-5 flex items-center gap-4 text-ink-2"><Circle tone="ok"><Icon name="check" size={18} /></Circle>
                {done.length ? "All done for today." : "Nothing needs you today."} {later.length ? `Later has ${later.length} more; add one if you have time.` : ""}</p>
            ) : (
              <ol className="mt-5 grid gap-6">
                {open.slice(0, 4).map((it, i) => {
                  const tn = tone(it);
                  return (
                    <li key={it.id} className="finding relative grid grid-cols-[32px_minmax(0,1fr)] gap-4 not-last:before:absolute not-last:before:top-10 not-last:before:-bottom-4 not-last:before:left-[15.5px] not-last:before:w-px not-last:before:bg-border">
                      <Circle tone={tn} hidden>{it.n}</Circle>
                      <div>
                        <h3 className="text-lg font-semibold"><span className="sr">{tn === "fail" ? "Production failure: " : tn === "warn" ? "Needs attention: " : "Notice: "}</span><a className="text-foreground" href={it.href}>{it.title}</a></h3>
                        <p className="mt-1 line-clamp-2 text-ink-2">{it.whyNow}</p>
                        <div className="mt-3 flex flex-wrap items-center justify-between gap-3">
                          <span className="flex flex-wrap gap-1.5"><Pill tone="neutral" title={"Estimate: " + it.estBasis}>{minutes(it.estMin)}</Pill><Flags it={it} /></span>
                          {i === 0 && act && it.status === "planned" ? (
                            <form action={todayAct}>
                              <Hidden csrf={csrf} action="start" item={it.id} />
                              <Button size="sm">Start</Button>
                            </form>
                          ) : (
                            <a className="go-link" href={it.href}>{it.linkLabel} <Icon name="arrow" size={14} /></a>
                          )}
                        </div>
                      </div>
                    </li>
                  );
                })}
              </ol>
            )}
            </CardContent>
            {open.length > 4 ? <CardFooter className="mt-5 text-sm"><FootLink href="#plan">{open.length - 4} more in today&apos;s plan</FootLink></CardFooter> : null}
          </Card>

            <Card as="section" className="gap-0" id="plan" aria-labelledby="plan-h">
              <CardHeader>
                <CardTitle><h2 id="plan-h">Plan for today</h2></CardTitle>
                <CardDescription>In order · {minutes(Math.max(0, p.plannedMin - p.doneMin))} still to do{act ? " · mark each one done here" : ""}</CardDescription>
              </CardHeader>
              <CardContent>
              {open.length === 0 ? <p className="empty">Nothing left in today&apos;s plan.</p> : (
                <ol className="mt-3">
                  {open.map((it) => (
                    <li key={it.id} id={`t-${it.id}`} className="plan-row">
                      {act ? (
                        <form action={todayAct} className="grid justify-items-center pt-0.5">
                          <Hidden csrf={csrf} action="done" item={it.id} />
                          <button className="check-btn" aria-label={`Mark done: ${it.title}`} title="Mark done"><Icon name="check" size={18} /></button>
                        </form>
                      ) : <span className="check-ro justify-self-center" aria-hidden="true">{it.n}</span>}
                      <div className="grid min-w-0 gap-1">
                        <a className="font-semibold text-foreground" href={it.href}>{it.title}</a>
                        <p className="text-sm text-ink-2"><span className="text-muted-foreground">{admin ? "Do this:" : "An admin's next step:"}</span> {it.doThis}</p>
                        <div className="flex flex-wrap gap-1.5"><Flags it={it} /></div>
                        <details className="fold mt-0.5">
                          <summary>Details</summary>
                          <div className="grid gap-3 pt-3 pb-1">
                            <p className="text-sm text-ink-2"><b>Why now:</b> {it.whyNow}</p>
                            <dl className="kv">
                              <dt>Estimate</dt><dd>{minutes(it.estMin)} · {it.estBasis}</dd>
                              <dt>Score</dt><dd className="num">{it.score.toFixed(1)} = urgency {it.urgency.toFixed(1)} × impact {it.impact.toFixed(1)} × confidence {it.confidence.toFixed(2)} ÷ effort {it.score > 0 ? ((it.urgency * it.impact * it.confidence) / it.score).toFixed(2) : "-"}</dd>
                              {it.details.length ? null : <><dt>Source</dt><dd className="mono">{it.ref}</dd></>}
                            </dl>
                            {it.details.length ? (
                              <ul className="grid gap-1 text-sm">{it.details.map((d, i) => <li key={i}>{d.href ? <a href={d.href}>{d.text}</a> : d.text}</li>)}</ul>
                            ) : null}
                            {it.promptUrl ? <p><a href={it.promptUrl}>Download the fix prompt (.md)</a></p> : null}
                            {act ? (
                              <div className="grid gap-3 pt-1">
                                <form action={todayAct} className="plan-form">
                                  <Hidden csrf={csrf} action="skip" item={it.id} />
                                  <Field className="min-w-40 flex-1" label={<>Reason <small className="font-normal text-muted-foreground">(optional)</small></>}><Input name="reason" maxLength={200} autoComplete="off" /></Field>
                                  <Button variant="outline" size="sm">Skip today</Button>
                                </form>
                                <form action={todayAct} className="plan-form">
                                  <Hidden csrf={csrf} action="defer" item={it.id} />
                                  <Field className="min-w-40 flex-1" label="Defer for">
                                    <Select className="w-full" name="days" defaultValue="3"><option value="1">1 day</option><option value="3">3 days</option><option value="7">a week</option><option value="30">a month</option></Select>
                                  </Field>
                                  <Button variant="outline" size="sm">Defer</Button>
                                </form>
                                <form action={todayAct} className="plan-form">
                                  <Hidden csrf={csrf} action="done" item={it.id} />
                                  <Field className="min-w-40 flex-1" label="It took (min)"><Input name="minutes" type="number" min={1} max={600} placeholder={String(it.estMin)} /></Field>
                                  <Button variant="outline" size="sm">Done</Button>
                                </form>
                              </div>
                            ) : null}
                          </div>
                        </details>
                      </div>
                      <div className="plan-end">
                        <span className="num muted" title={"Estimate: " + it.estBasis}>{minutes(it.estMin)}</span>
                        <a className="go-link" href={it.href}>{it.linkLabel} <Icon name="arrow" size={14} /></a>
                      </div>
                    </li>
                  ))}
                </ol>
              )}
              </CardContent>
              {done.length || later.length ? (
                <CardFooter className="mt-5 grid gap-2.5 text-sm">
                  {done.length ? (
                    <details className="fold">
                      <summary>Done today <span className="ncount">{done.length}</span>
                        {p.actualMin > 0 ? <span className="muted"> took {minutes(p.actualMin)}, estimated {minutes(p.doneMin)}</span> : null}</summary>
                      <ul className="mt-2">
                        {done.map((it) => (
                          <li key={it.id} className="plan-mini">
                            <span className="inline-flex text-ok-foreground"><Icon name="check" size={14} /></span>
                            <a href={it.href}>{it.n}. {it.title}</a>
                            <span className="muted min-w-40 flex-1">est {minutes(it.estMin)}{it.autoDone ? ` · ${it.note || "closed at the source"}` : it.actualMin ? ` · took ${minutes(it.actualMin)}` : ""}</span>
                            {it.closeHref ? <a className="small" href={it.closeHref}>{it.closeLabel}</a> : null}
                            {act ? (
                              <form className="ml-auto" action={todayAct}><Hidden csrf={csrf} action="undo" item={it.id} /><Button variant="ghost" size="sm">Undo</Button></form>
                            ) : null}
                          </li>
                        ))}
                      </ul>
                    </details>
                  ) : null}
                  {later.length ? (
                    <details className="fold">
                      <summary>Later <span className="ncount">{later.length}</span><span className="muted"> did not fit today; the reason is on each line</span></summary>
                      <ul className="mt-2">
                        {later.slice(0, 15).map((it) => (
                          <li key={it.id} className="plan-mini">
                            <a href={it.href}>{it.title}</a>
                            <span className="muted min-w-40 flex-1">{minutes(it.estMin)} · {it.laterReason || it.status}</span>
                            {act ? (
                              <form className="ml-auto" action={todayAct}><Hidden csrf={csrf} action="today" item={it.id} /><Button variant="ghost" size="sm" title="Add it to today's plan">Do today</Button></form>
                            ) : null}
                          </li>
                        ))}
                      </ul>
                      {later.length > 15 ? <p className="muted small">Showing 15 of {later.length}, best first.</p> : null}
                    </details>
                  ) : null}
                </CardFooter>
              ) : null}
            </Card>

          </div>
          <div className="grid min-w-0 gap-4">
            {t.incidents ? <IncidentsCard d={t.incidents} /> : null}
            {t.checks ? <ChecksCard c={t.checks} /> : null}
            <Card as="section" className="gap-0" aria-labelledby="rv-h">
              <CardHeader>
                <CardTitle><h2 id="rv-h">Review inbox</h2></CardTitle>
                <CardDescription>Decisions waiting for you · most urgent first</CardDescription>
              </CardHeader>
              <CardContent>
              {rv.total === 0 ? <p className="empty">No decisions waiting. Argus proposes new tasks after each run.</p> : (
                <>
                  <p className="figure mt-5 text-ink-2"><b className="num mr-1 text-2xl font-semibold text-foreground">{rv.total}</b> waiting{admin ? <span className="muted"> · ~{minutes(rv.estMin)}</span> : null}</p>
                  <ul className="mt-3">
                    {rv.top.map((x) => (
                      <li key={x.sel}>
                        <a data-slot="mini-link" className="mini-q" href={`/review?sel=${x.sel}`}>
                          <SevMark sev={x.sevKey} />
                          <span className="truncate text-sm font-semibold">{x.title}</span>
                          <span className="text-xs text-muted-foreground">{x.kindRow}{x.envLabel ? ` · ${x.prod ? "prod" : x.envLabel}` : ""} · {x.waiting}</span>
                        </a>
                      </li>
                    ))}
                  </ul>
                </>
              )}
              </CardContent>
              <CardFooter className="mt-5 text-sm"><FootLink href="/review">View review inbox</FootLink></CardFooter>
            </Card>
          </div>
          </div>

          {t.waiting?.length ? (
            <Card as="section" className="gap-0" aria-labelledby="wait-h">
              <CardHeader>
                <CardTitle><h2 id="wait-h">{admin ? "Waiting on you" : "Waiting on the operator"}</h2></CardTitle>
                <CardAction><Pill tone="neutral" title="Decisions waiting, across these groups">{t.waiting.reduce((n, w) => n + w.count, 0)}</Pill></CardAction>
                <CardDescription>Open decisions the planner keeps out of the plan</CardDescription>
              </CardHeader>
              <CardContent>
              <ul className="mt-2">
                {t.waiting.map((w, i) => (
                  <li key={i} className="plan-mini">
                    <Pill tone="neutral">{w.group}</Pill>
                    {w.href ? <a href={w.href}>{w.title}</a> : <span>{w.title}</span>}
                    {w.count ? <span className="ncount">{w.count}</span> : null}
                    <span className="muted min-w-40 flex-1">{w.detail}</span>
                  </li>
                ))}
              </ul>
              </CardContent>
            </Card>
          ) : null}

          {act ? (
            <Card as="details" className="fold block px-(--card-spacing)">
              <summary className="text-base! text-foreground!">Plan settings <span className="muted ml-1.5 font-normal">{hours(p.budgetMin)} focus · {plural(p.maxItems, "task")} · {plural(p.maxRisky, "production change")}</span></summary>
              <form action={todayAct} className="mt-4 flex flex-wrap items-end gap-3">
                <Hidden csrf={csrf} action="settings" />
                <Field label="Focus time (min)"><Input type="number" name="focus_min" min={15} max={600} defaultValue={p.budgetMin} /></Field>
                <Field label="At most (tasks)"><Input type="number" name="max_items" min={1} max={20} defaultValue={p.maxItems} /></Field>
                <Field label="Changes that need a window"><Input type="number" name="max_risky" min={0} max={3} defaultValue={p.maxRisky} /></Field>
                <Button variant="outline">Save and re-plan</Button>
              </form>
            </Card>
          ) : null}
        </>
      )}
    </>
  );
}

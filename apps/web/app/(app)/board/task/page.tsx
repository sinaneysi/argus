import { HelpQ } from "@/components/HelpQ";
import type { Metadata } from "next";
import { notFound } from "next/navigation";
import { agentAct, boardAssign, boardComment, boardMove as moveAction } from "../actions";
import { Reload } from "@/components/Infra";
import { Markdown } from "@/components/Markdown";
import { EnvPill, Pill } from "@/components/Pills";
import { Banners, Crumb, FixCards, pick, Report, type Search, tone, When } from "@/components/Section";
import { AgentForm, ChatBox, FixerBox, MoveChoiceForm, MoveForm, Progress, SecretsBox, SpinBar } from "@/components/Work";
import { qs } from "@/lib/api";
import { full, iso } from "@/lib/format";
import { apiGet, csrfToken, readFlash } from "@/lib/server";
import type { Agent, Banner, TaskPage } from "@/lib/types";
import { Button, buttonVariants } from "@/components/ui/button";
import { PlainCard } from "@/components/ui/card";

export const metadata: Metadata = { title: "Task" };

function Result({ a, t, csrf }: { a: Agent; t: TaskPage; csrf: string }) {
  const r = a.result;
  if (!r) return null;
  const lists: [string, string[]][] = [["Evidence", r.evidence], ["Rollback", r.rollback], ["Needs from you", r.needs]];
  return (
    <>
      <h3>Result</h3><p dir="auto">{r.summary}</p>
      {r.mr ? <p><a href={r.mr} rel="noopener noreferrer">Merge request</a> <span className="muted small">(never merged by Argus)</span></p> : null}
      {r.fixRun ? <p><a href={`/admin/fix/run?id=${encodeURIComponent(r.fixRun)}`}>Fix run {r.fixRun}</a></p> : null}
      {lists.map(([h, l]) => (l.length ? <div key={h}><h3>{h}</h3><ul className="ag-l">{l.map((x, i) => <li key={i} dir="auto">{x}</li>)}</ul></div> : null))}
      {r.report ? <details className="fold"><summary>Full report</summary><Report doc={r.report} /></details> : null}
      {r.moves?.length ? (
        <form action={moveAction} className="actionbar">
          <input type="hidden" name="csrf" value={csrf} /><input type="hidden" name="key" value={a.key} /><input type="hidden" name="back" value="task" /><input type="hidden" name="return" value={t.return} />
          {r.moves.map((m) => <Button key={m.col} name="col" value={m.col} variant={m.primary ? "default" : "outline"}>{m.label}</Button>)}
          <span className="muted small">Argus does not move the issue by itself.</span>
        </form>
      ) : null}
    </>
  );
}

function Plan({ a, t, csrf }: { a: Agent; t: TaskPage; csrf: string }) {
  const p = a.plan;
  return (
    <>
      {p ? <>
        <p dir="auto">{p.summary}</p>
        <p className="meta-line">{p.env ? <EnvPill env={p.env} /> : null}<Pill>{p.kind}</Pill>{p.estimate ? <span className="small">{p.estimate}</span> : null}</p>
        {p.prodNote ? <Banners list={[{ kind: "info", text: "Production: Argus never executes this. Write proposal produces the full change proposal (command, backup, rollback, verify) for a human." }]} /> : null}
        <h3>Plan</h3>
        <ol className="ag-plan">{p.steps.map((s, i) => <li key={i}><p dir="auto">{s.text}</p><p className="meta-line"><Pill tone={s.mode === "WRITE" ? "warn" : "neutral"}>{s.mode}</Pill>{s.targets.map((x) => <span key={x} className="mono small">{x}</span>)}</p></li>)}</ol>
        {p.needs.length ? <><h3>Needs from you</h3><ul className="ag-l">{p.needs.map((x, i) => <li key={i} dir="auto">{x}</li>)}</ul></> : null}
      </> : null}
      {a.questions.length && a.chatOn ? <>
        <h3>Questions <span className="ncount">{a.questions.length}</span></h3>
        <ol className="ag-plan">{a.questions.map((q) => <li key={q.id}><p dir="auto">{q.text}{q.blocking ? null : <span className="muted"> (optional)</span>}</p><p className="muted small" dir="auto">{q.answer ? `Answer: ${q.answer}` : "Not answered yet: answer in the chat"}</p></li>)}</ol>
      </> : null}
      {a.emptyDesc ? <Banners list={[{ kind: "warn", text: "The issue description is empty. Tell Argus what to do; it plans again from that." }]} /> : null}
      {a.pause ? <p><a data-slot="button" className={buttonVariants({ variant: "outline" })} href={a.pause.href}>{a.pause.text}</a> <span className="muted small">The page stops refreshing while you write.</span></p> : null}
      <AgentForm a={a} csrf={csrf} ret={t.return} />
      <SpinBar a={a} csrf={csrf} ret={t.return} />
    </>
  );
}

function AgentPanel({ a, t, csrf }: { a: Agent; t: TaskPage; csrf: string }) {
  return (
    <>
      <PlainCard as="section" className="agent" id="agent" aria-labelledby="ag-h">
        <div className="card-h"><h2 id="ag-h">Argus works this task</h2><Pill tone={tone(a.kind)}>{a.label}</Pill></div>
        {a.meta.length ? <p className="muted small">{a.meta.join(" · ")}</p> : null}
        {a.notices?.length ? <Banners list={a.notices} /> : null}
        {a.replan ? (a.replan.can
          ? <form action={agentAct} className="actionbar"><input type="hidden" name="csrf" value={csrf} /><input type="hidden" name="key" value={a.key} /><input type="hidden" name="return" value={t.return} /><Button variant={a.replan.quiet ? "outline" : "default"} name="action" value="replan">Plan again</Button><span className="muted small">{a.replan.why}</span></form>
          : <div className="actionbar"><Button variant="outline" disabled>Plan again</Button><span className="muted small">{a.replan.why}</span></div>) : null}
        {a.facts?.length ? <details className="fold"><summary>What failed exactly</summary><dl className="rec wide">{a.facts.map((f) => <div key={f.label}><dt>{f.label}</dt><dd className={f.mono ? "mono" : undefined}>{f.href ? <a href={f.href}>{f.value}</a> : f.value}</dd></div>)}</dl><p className="muted small">Every attempt stays in the run log below.</p></details> : null}
        {a.progress ? <Progress p={a.progress} /> : null}
        {a.finished ? <>
          <Result a={a} t={t} csrf={csrf} />
          <details className="fold"><summary>Plan v{a.planVersion}, questions and instructions</summary><Plan a={a} t={t} csrf={csrf} /></details>
        </> : <>
          <Plan a={a} t={t} csrf={csrf} />
          <Result a={a} t={t} csrf={csrf} />
        </>}
      </PlainCard>
      {a.plan?.fix ? <FixCards f={a.plan.fix} quiet /> : null}
      {a.chat ? <ChatBox c={a.chat} csrf={csrf} ret={t.return} /> : null}
      {a.secrets ? <SecretsBox s={a.secrets} k={a.key} csrf={csrf} ret={t.return} /> : null}
      {a.log.length ? <PlainCard as="details" className="fold-t"><summary>Run log <span className="ncount">{a.log.length}</span></summary><ol className="tl">{a.log.map((l, i) => <li key={i} className="ev-other"><p className="tl-h"><time dateTime={iso(l.at)}>{full(l.at)}</time> <Pill>{l.kind}</Pill></p><p dir="auto">{l.text}</p></li>)}</ol></PlainCard> : null}
    </>
  );
}

const Code = ({ label, text }: { label: string; text: string }) => <pre className="code prompt" role="region" aria-label={label} tabIndex={0}><code>{text}</code></pre>;

export default async function TaskPageView({ searchParams }: { searchParams: Search }) {
  const params = await pick(searchParams, ["id", "key", "req", "live", "sec"]);
  const [r, csrf, flash] = await Promise.all([apiGet<TaskPage>("/api/v1/board/task" + qs(params)), csrfToken(), readFlash()]);
  if (r.status === 404) notFound();
  if (r.status !== 200) throw new Error(`/api/v1/board/task answered ${r.status}`);
  const t = r.data;
  const list: Banner[] = [...t.notices];
  if (t.gone) list.push({ kind: "warn", text: t.gone });
  if (t.prod) list.push({ kind: "info", text: "Production: every step is a proposal for a human; each change needs the operator's explicit approval. Argus never runs it." });
  return (
    <>
      <Reload r={t.refresh} />
      <header className="phead"><div><Crumb href="/board" label="Board" /><h1 dir="auto">{t.title}</h1><HelpQ p="/board" />
        <p className="meta-line"><Pill>{t.status}</Pill><span>{t.tag} <span className="mono">{t.ref}</span></span>{t.env ? <EnvPill env={t.env} /> : null}<span>{t.owner ? `assigned to ${t.owner}` : "Unassigned"}</span>{t.priority ? <span>{t.priority}</span> : null}<span>opened <When ts={t.openedAt} /></span></p></div></header>
      <Banners list={list} flash={flash} />
      {t.choice ? <MoveChoiceForm c={t.choice} csrf={csrf} ret={t.return} /> : null}
      <div className="actionbar">
        {t.move ? <MoveForm m={t.move} csrf={csrf} ret={t.return} back={t.move.kind === "jira" ? "task" : undefined} label="Move to" /> : null}
        {t.assign ? <form action={boardAssign}><input type="hidden" name="csrf" value={csrf} /><input type="hidden" name="task" value={t.id} /><input type="hidden" name="return" value={t.return} /><Button variant="outline">Assign to me</Button></form> : null}
        {t.links.map((l) => <a key={l.href} data-slot="button" className={buttonVariants({ variant: "outline" })} href={l.href} rel="noopener noreferrer">{l.text}</a>)}
        {t.readOnly ? <span className="muted small">{t.readOnly}</span> : null}
      </div>
      <div className="detail">
        <div className="stack">
          <PlainCard as="section" aria-labelledby="de-h"><h2 id="de-h" className="h-card">Description</h2>{t.description ? <div className="md"><Markdown text={t.description} /></div> : <p className="muted">No description.</p>}
            {t.evidence?.fold ? <details className="fold"><summary>Evidence <span className="muted">from {t.evidence.from}</span></summary><Code label="Evidence text" text={t.evidence.text} /></details> : null}</PlainCard>
          {t.evidence && !t.evidence.fold ? <PlainCard as="section" aria-labelledby="ev-h"><h2 id="ev-h" className="h-card">Evidence <span className="muted">from {t.evidence.from}</span></h2><Code label="Evidence text" text={t.evidence.text} /></PlainCard> : null}
          {t.agent ? <AgentPanel a={t.agent} t={t} csrf={csrf} /> : null}
          {t.fixPrompt ? <PlainCard as="section" aria-labelledby="fp2-h"><h2 id="fp2-h" className="h-card">Fix prompt</h2><p className="muted small">Review it fully before it goes to the fixer.</p><Code label="Fix prompt text" text={t.fixPrompt} /></PlainCard> : null}
          <FixerBox t={t} csrf={csrf} />
          <PlainCard as="section" id="comments" aria-labelledby="cm-h">
            <h2 id="cm-h" className="h-card">Comments <span className="ncount">{t.comments.count}</span></h2>
            {t.comments.items.length ? <ol className="cm">{t.comments.items.map((c, i) => <li key={i}><p className="tl-h"><b>{c.author}</b> <When ts={c.at} />{c.pending ? <> <Pill>sending</Pill></> : null}</p><p dir="auto" className="pre">{c.body}</p></li>)}</ol> : <p className="muted">No comment yet.</p>}
            {t.comments.blocked ? <p className="muted small">{t.comments.blocked}</p> : (
              <form action={boardComment} className="ag-f"><input type="hidden" name="csrf" value={csrf} /><input type="hidden" name="task" value={t.id} /><input type="hidden" name="return" value={t.return} />
                <label className="field"><span className="sr">Comment</span><textarea name="body" rows={3} maxLength={t.comments.max} required placeholder={`Add a comment (sent to ${t.comments.tracker})`} dir="auto" /></label>
                <div><Button variant="outline">Comment</Button></div></form>
            )}
          </PlainCard>
          <PlainCard as="details" className="fold-t"><summary>History <span className="ncount">{t.history.length}</span></summary>
            {t.history.length ? <ol className="tl">{t.history.map((e, i) => <li key={i} className={`ev-${e.kind}`}><p className="tl-h"><time dateTime={iso(e.at)}>{full(e.at)}</time> <Pill>{e.tag}</Pill> <span className="muted">{e.actor}</span></p><p dir="auto">{e.text}</p></li>)}</ol> : <p className="muted">No history yet.</p>}
          </PlainCard>
        </div>
        <aside>
          <PlainCard as="section" className="rail-c"><h2>Record</h2>
            <dl className="rec">{t.record.map((f) => <div key={f.label}><dt>{f.label}</dt><dd className={f.mono ? "mono" : undefined} dir="auto">{f.href ? <a href={f.href} rel={f.href.startsWith("http") ? "noopener noreferrer" : undefined}>{f.value}</a> : f.value}</dd></div>)}
              <div><dt>Labels</dt><dd>{t.labels.length ? <span className="tags">{t.labels.map((l) => <a key={l} className="chip" href={`/board?label=${encodeURIComponent(l)}`}>{l}</a>)}</span> : "-"}</dd></div></dl>
          </PlainCard>
        </aside>
      </div>
    </>
  );
}

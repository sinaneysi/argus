import "./work.css";
import { Fragment } from "react";
import { EnvPill, Pill } from "./Pills";
import { Segs, tone } from "./Section";
import { agentAct, agentChat, agentSpin, boardFixer, boardMove, taskSecret } from "@/app/(app)/board/actions";
import { full, iso } from "@/lib/format";
import type { Agent, Board, BoardCard, Chat, Chip, FilterRow, MoveChoice, MoveCtl, QaMsg, Seg, SecretForm, Secrets, TaskPage } from "@/lib/types";
import { Button, buttonVariants } from "@/components/ui/button";
import { PlainCard } from "@/components/ui/card";

// The parts the Work pages share. Server components: forms post through server actions, folds are <details>, and a
// page that follows live work reloads with the same meta refresh the Go page used, so the CSP needs nothing extra.

function ChipLink({ c }: { c: Chip }) {
  return <a className={c.on ? "chip on" : "chip"} href={c.href} title={c.title || undefined} aria-current={c.on ? "true" : undefined}>{c.on ? <span aria-hidden="true">✓ </span> : null}{c.label}</a>;
}

function Row({ r }: { r: FilterRow }) {
  return (
    <div className="f-row" role="group" aria-label={r.label}><span className="f-l">{r.label}</span>
      <div className="chips">{r.chips.map((c) => <ChipLink key={c.href + c.label} c={c} />)}
        {r.folds.map((f) => <details key={f.summary} className="f-all" open={f.open}><summary>{f.summary} ({f.chips.length})</summary><div className="chips">{f.chips.map((c) => <ChipLink key={c.href + c.label} c={c} />)}</div></details>)}</div>
    </div>
  );
}

/** Every filter is a link that changes one query parameter: a click applies it, nothing to submit. */
export function BoardChips({ d }: { d: Board }) {
  if (d.chips) return <nav className="chips qf" aria-label="Quick filters">{d.chips.map((c) => <ChipLink key={c.href + c.label} c={c} />)}</nav>;
  const f = d.filters;
  if (!f) return null;
  return (
    <nav className="bf" aria-label="Filters">
      {f.main.map((r) => <Row key={r.label} r={r} />)}
      <details className="f-more" open={f.advanced > 0}><summary>More filters{f.advanced ? <b> · {f.advanced} set</b> : null}</summary>{f.more.map((r) => <Row key={r.label} r={r} />)}</details>
      {f.active.length ? <div className="f-row" role="group" aria-label="Active filters"><span className="f-l">Active</span><div className="chips">{f.active.map((c) => <a key={c.href} className="chip on" href={c.href} aria-label={`Remove filter ${c.label}`}>{c.label}<span aria-hidden="true"> ×</span></a>)}<a className="chip" href={f.clear}>Clear all</a></div></div> : null}
      {d.results ? <p className="muted small">{d.results}</p> : null}
    </nav>
  );
}

export function MoveForm({ m, csrf, ret, back, label }: { m: MoveCtl; csrf: string; ret: string; back?: string; label: string }) {
  const name = m.kind === "jira" ? "col" : "to";
  return (
    <form action={boardMove} className="mv">
      <input type="hidden" name="csrf" value={csrf} /><input type="hidden" name="return" value={ret} />
      {m.kind === "jira" ? <input type="hidden" name="key" value={m.key} /> : <input type="hidden" name="task" value={m.task} />}
      {back ? <input type="hidden" name="back" value={back} /> : null}
      <select name={name} aria-label={label} defaultValue="" required><option value="" disabled>Move to…</option>{m.options.map((o) => <option key={o.value} value={o.value}>{o.label}</option>)}</select>
      <Button variant="outline" size="sm">Move</Button>
    </form>
  );
}

export function MoveChoiceForm({ c, csrf, ret }: { c: NonNullable<MoveChoice>; csrf: string; ret: string }) {
  return (
    <PlainCard as="form" action={boardMove} className="mv-choice">
      <p>Jira offers more than one way to move <span className="mono">{c.key}</span> there. Which one?</p>
      <input type="hidden" name="csrf" value={csrf} /><input type="hidden" name="return" value={ret} /><input type="hidden" name="key" value={c.key} /><input type="hidden" name="col" value={c.col} />
      <div className="actionbar">{c.options.map((o) => <Button key={o.value} variant="outline" name="transition" value={o.value}>{o.label}</Button>)}</div>
    </PlainCard>
  );
}

/** The fold label of a plan's write steps: the card stays short, the warning stays visible. */
function writeCount(p: NonNullable<BoardCard["plan"]>) {
  const n = (p.writes?.length ?? 0) + (p.more ?? 0);
  return n === 1 ? "1 step changes something" : `${n} steps change something`;
}

function Plan({ p }: { p: NonNullable<BoardCard["plan"]> }) {
  if (p.wait) return <p className="kc-plan muted">{p.wait}</p>;
  const open = p.open ? <b className="warn-t">{p.open} open question{p.open === 1 ? "" : "s"}</b> : null;
  const meta = (p.meta ?? []).join(" · ");
  if (p.writes?.length) {
    return (
      <div className="kc-agent">
        <details className="kc-wf fold"><summary>{p.env ? <EnvPill env={p.env} /> : null}<Pill tone="warn">WRITE</Pill> {writeCount(p)}</summary>
          {p.kind ? <p className="kc-m"><Pill>{p.kind}</Pill></p> : null}
          <p className="kc-plan" dir="auto">{p.summary}</p>
          <ul className="kc-w">{p.writes.map((w, i) => <li key={i}>{w.text} {w.targets.length ? <span className="mono">{w.targets.join(", ")}</span> : null}</li>)}{p.more ? <li className="muted">and {p.more} more</li> : null}</ul>
          {meta ? <p className="kc-m">{meta}</p> : null}
        </details>
        {open ? <p className="kc-m">{open}</p> : null}
      </div>
    );
  }
  return (
    <div className="kc-agent">
      <p className="kc-m">{p.env ? <EnvPill env={p.env} /> : null}{p.kind ? <Pill>{p.kind}</Pill> : null}</p>
      <p className="kc-plan" dir="auto">{p.summary}</p>
      <p className="kc-m">{meta}{open ? <> · {open}</> : null}</p>
    </div>
  );
}

function Card({ k, csrf, ret }: { k: BoardCard; csrf: string; ret: string }) {
  return (
    <li className="kcard">
      <div className="kc-top"><span className="kc-tag" title={k.tag}>{k.tag}</span><span className="mono kc-key" title={k.ref}>{k.ref}</span>{k.agent ? (k.href ? <a className="kc-ag" href={`${k.href}#agent`} title="Open the task's agent panel"><Pill tone={tone(k.agent.kind)}>{k.agent.text}</Pill></a> : <Pill tone={tone(k.agent.kind)}>{k.agent.text}</Pill>) : null}</div>
      {k.href ? <a className="kc-t" href={k.href} title={k.title} dir="auto">{k.title}</a> : <span className="kc-t" dir="auto">{k.title}</span>}
      <div className="kc-o">{k.owner ? <span className="kc-who"><span className="kc-av" aria-hidden="true">{k.owner.charAt(0).toUpperCase()}</span><span className="kc-nm" title={k.owner}>{k.owner}</span></span> : <span className={k.open ? "kc-who none" : "kc-who"}>Unassigned</span>}
        {k.priority ? <span className={k.priorityHigh ? "kc-pri hi" : "kc-pri"}>{k.priority}</span> : null}</div>
      {k.env || k.source || k.auto || k.age || k.fixPrompt ? (
        <div className="kc-m">{k.env ? <EnvPill env={k.env} /> : null}
          {k.source ? (k.source.href ? <a className="kc-src" href={k.source.href} title={k.source.text}>{k.source.text}</a> : <span className="kc-src" title={k.source.text}>{k.source.text}</span>) : null}
          {k.auto ? <span className="kc-src" title="created automatically by Argus">auto</span> : null}
          {k.fixPrompt ? <a className="kc-src" href={k.href} title="Evidence and fix prompt">fix prompt</a> : null}
          {k.age ? <span className="kc-age" title={k.at ? `${k.verb} ${full(k.at)}` : undefined}>{k.verb} {k.age}</span> : null}</div>
      ) : null}
      {k.badges.length || k.fix ? <div className="kc-m">{k.badges.map((b) => <Pill key={b.text} tone={tone(b.kind)}>{b.text}</Pill>)}{k.fix ? <Pill tone={tone(k.fix.kind)} title={k.fix.title || undefined}>{k.fix.text}</Pill> : null}</div> : null}
      {k.plan ? <Plan p={k.plan} /> : null}
      {k.move ? <details className="kc-mv"><summary aria-label={`Move ${k.ref}`}>Move</summary><MoveForm m={k.move} csrf={csrf} ret={ret} back={k.move.kind === "task" ? "board" : undefined} label={`Move ${k.ref} to`} /></details> : null}
    </li>
  );
}

export function Columns({ d, csrf }: { d: Board; csrf: string }) {
  return (
    <>
      <nav className="kjump" aria-label="Columns">{d.columns.map((c) => <a key={c.id} className="chip" href={`#col-${c.id}`}>{c.name} <b>{c.count}</b></a>)}</nav>
      <div className="kanban" role="region" aria-label="Board columns" tabIndex={0}>
        {d.columns.map((c) => (
          <section key={c.id} className={c.over ? "kcol over" : c.under ? "kcol under" : "kcol"} id={`col-${c.id}`} aria-labelledby={`h-${c.id}`}>
            <div className="kcol-h"><h2 id={`h-${c.id}`} title={c.title || undefined}>{c.name}</h2><span className="ncount">{c.count}</span>{c.limits ? <span className="wip">{c.over ? "Over limit · " : c.under ? "Under limit · " : ""}{c.limits}</span> : null}</div>
            <ol className="kcards">
              {c.cards.map((k) => <Card key={k.ref} k={k} csrf={csrf} ret={d.return} />)}
              {!c.cards.length ? <li className="kc-empty">{c.empty}{c.help ? <span className="small">{c.help}</span> : null}</li> : null}
              {c.more ? <li className="kc-empty">{c.more.hidden} more · <a href={c.more.href}>Show all {c.count}</a></li> : null}
            </ol>
          </section>
        ))}
      </div>
    </>
  );
}

const Hid = ({ csrf, k, ret }: { csrf: string; k: string; ret: string }) => <><input type="hidden" name="csrf" value={csrf} /><input type="hidden" name="key" value={k} /><input type="hidden" name="return" value={ret} /></>;

export function ChatBox({ c, csrf, ret }: { c: Chat; csrf: string; ret: string }) {
  return (
    <PlainCard as="section" id="chat" aria-labelledby="chat-h">
      <h2 id="chat-h" className="h-card">Chat with Argus</h2>
      {c.messages.length ? (
        <ol className="tl">{c.messages.map((m) => (
          <li key={m.seq} id={`m${m.seq}`} className={m.argus ? "ev-argus" : undefined}>
            <p className="tl-h"><time dateTime={iso(m.at)}>{full(m.at)}</time> <b>{m.who}</b></p>
            <p dir="auto" className="pre">{m.body}{m.proposal ? <> → <a href="/review?kind=proposals">decide in Review</a></> : null}</p>
          </li>
        ))}</ol>
      ) : c.empty ? <p className="muted">{c.empty}</p> : null}
      {c.pause ? <p><a data-slot="button" className={buttonVariants({ variant: "outline" })} href={c.pause.href}>{c.pause.text}</a> <span className="muted small">The page stops refreshing while you write.</span></p> : null}
      {c.canPost && !c.pause ? (
        <form action={agentChat} className="ag-f">
          <Hid csrf={csrf} k={c.key} ret={ret} />
          <label className="field"><span>Message</span><textarea name="text" rows={3} maxLength={4000} required disabled={c.pending} dir="auto" /></label>
          <div className="tf-save"><Button  disabled={c.pending}>{c.pending ? "Argus is replying…" : "Send"}</Button>{c.note ? <span className="muted small">{c.note}</span> : null}</div>
        </form>
      ) : !c.canPost && c.note ? <p className="muted small">{c.note}</p> : null}
    </PlainCard>
  );
}

function SecretFormBox({ f, k, csrf, ret }: { f: SecretForm; k: string; csrf: string; ret: string }) {
  return (
    <form action={taskSecret} className="ag-f" autoComplete="off">
      <Hid csrf={csrf} k={k} ret={ret} />
      <label className="field"><span>Name</span><input name="name" defaultValue={f.name} pattern="[A-Z][A-Z0-9_]{1,31}" maxLength={32} required placeholder="API_KEY" /></label>
      <label className="field"><span>Value</span><input type="password" name="value" autoComplete="off" maxLength={8192} required /></label>
      <label className="field"><span>Hosts it may be used against (comma separated)</span><input name="hosts" defaultValue={f.hosts} maxLength={600} required /></label>
      <label className="field"><span>Expires after (minutes, at most 1440)</span><input type="number" name="ttl_min" min={1} max={1440} defaultValue={f.ttl} required /></label>
      <label className="field"><span>Purpose (optional)</span><input name="purpose" defaultValue={f.purpose} maxLength={300} /></label>
      <div className="tf-save"><Button>{f.label}</Button><span className="muted small">Stored only in memory on Argus&apos;s host, encrypted with a key for this task; the agent never sees it; wiped when the task ends.</span></div>
    </form>
  );
}

export function SecretsBox({ s, k, csrf, ret }: { s: Secrets; k: string; csrf: string; ret: string }) {
  return (
    <PlainCard as="section" id="secrets" aria-labelledby="sec-h">
      <h2 id="sec-h" className="h-card">Credentials</h2>
      {s.items.length ? <ul className="ag-sec">{s.items.map((i, n) => <li key={n}><b>{i.name}</b> {i.text}{i.rotate ? <b> Rotate recommended: it appeared in a command&apos;s output, redacted.</b> : null}{i.warn ? <b> {i.warn}</b> : null}{i.note ? <span className="muted small">{i.note}</span> : null}</li>)}</ul> : null}
      {s.note ? <p className="muted small">{s.note}</p> : null}
      {s.forms.map((f) => <SecretFormBox key={f.name} f={f} k={k} csrf={csrf} ret={ret} />)}
      {s.new ? <details className="fold"><summary>Provide secret</summary><SecretFormBox f={s.new} k={k} csrf={csrf} ret={ret} /></details> : null}
    </PlainCard>
  );
}

export function Progress({ p }: { p: NonNullable<Agent["progress"]> }) {
  return (
    <div className="ag-prog" role="status" aria-label={p.label}>
      {p.steps ? <ol className="ag-steps">{p.steps.map((w, i) => <li key={w} className={i < p.cur ? "done" : i === p.cur ? "now" : undefined} aria-current={i === p.cur ? "step" : undefined}>{i < p.cur ? <span className="sr">done: </span> : i > p.cur ? <span className="sr">to do: </span> : null}{w}</li>)}</ol> : null}
      {p.bar ? <div className="ag-bar" aria-hidden="true"><span /></div> : null}
      {p.now.length ? <p><b>Now:</b> <Segs s={p.now} /></p> : null}
      {p.meta.length ? <p className="muted small">{p.meta.map((m, i) => <Fragment key={i}>{i ? " · " : null}<Segs s={m} /></Fragment>)}</p> : null}
    </div>
  );
}

export function SpinBar({ a, csrf, ret }: { a: Agent; csrf: string; ret: string }) {
  const s = a.spin;
  if (!s) return <p className="muted small">{a.chatOn ? "Admins spin up the agent." : "Admins answer, instruct and spin up the agent."}</p>;
  if (!s.can) return <div className="actionbar"><Button  disabled title={s.reason}>{s.label}</Button>{s.reason ? <span className="muted small">{s.reason}</span> : null}</div>;
  return (
    <form action={agentSpin} className="actionbar">
      <Hid csrf={csrf} k={a.key} ret={ret} /><input type="hidden" name="plan_version" value={a.planVersion} />
      <Button variant={s.primary ? "default" : "outline"} title={`Approves exactly plan v${a.planVersion}`}>{s.label}</Button>
      <span className="muted small">Pinned to plan v{a.planVersion}; if the plan changes, review the new version and spin up again.{s.proposal ? " Production stays read-only: a read-only run writes the proposal and the task is blocked for the operator." : ""}</span>
    </form>
  );
}

export function AgentForm({ a, csrf, ret }: { a: Agent; csrf: string; ret: string }) {
  const i = a.inputs;
  const editable = !!i && !i.paused;
  if (!editable && !a.running) return null;
  return (
    <form action={agentAct} className="ag-f">
      <Hid csrf={csrf} k={a.key} ret={ret} />
      {editable ? <>
        {a.questions.map((q) => <label key={q.id} className="field"><span>{q.text}{q.blocking ? null : <small> (optional)</small>}</span><textarea name={`a_${q.id}`} rows={2} maxLength={2000} defaultValue={q.answer} disabled={!a.admin} dir="auto" /></label>)}
        <label className="field"><span>Instructions for the agent</span><textarea name="instructions" rows={a.emptyDesc ? 6 : 3} maxLength={8000} defaultValue={i.instructions} placeholder="What to do, where, what not to touch" disabled={!a.admin} dir="auto" /></label>
        {a.admin ? <label className="check"><input type="checkbox" name="post_jira" value="1" defaultChecked={i.postJira} />Post answers and instructions to {i.tracker} as a [Argus] comment</label> : null}
      </> : null}
      {a.admin ? <div className="tf-save">{editable ? <Button variant="outline" name="action" value="answers">Save answers &amp; re-plan</Button> : null}{a.running ? <Button variant="destructive" size="sm" name="action" value="stop" id="agent-stop">Stop</Button> : null}</div> : null}
    </form>
  );
}

export function FixerBox({ t, csrf }: { t: TaskPage; csrf: string }) {
  const f = t.fixer;
  if (!f) return null;
  return (
    <PlainCard as="section" aria-labelledby="fxr-h">
      <h2 id="fxr-h" className="h-card">{f.heading}</h2>
      {f.notes.map((n, i) => <p key={i} className="muted"><Segs s={n} /></p>)}
      {f.hosts ? (
        <form action={boardFixer} className="ag-f">
          <input type="hidden" name="csrf" value={csrf} /><input type="hidden" name="task" value={t.id} /><input type="hidden" name="return" value={t.return} />
          <p className="muted">Moves the task to Ready for fixer and hands the fix prompt above to the fixer on {f.envLabel}, with you as the approver. It runs one fix at a time; the result comes back here, and the task moves to Review.</p>
          <div className="hosts">{f.hosts.map((h) => <label key={h.alias} className="check"><input type="checkbox" name="host" value={h.alias} defaultChecked={h.checked} /><span className="mono">{h.alias}</span></label>)}</div>
          <div className="tf-save"><label className="check"><input type="checkbox" name="reviewed" value="1" required />I reviewed the fix prompt and the hosts</label><Button>Ready for fixer</Button></div>
        </form>
      ) : null}
    </PlainCard>
  );
}

// A chat message as the QA and run pages show it.
export function MsgBody({ m }: { m: QaMsg }) {
  if (m.noText) return <span className="muted">{m.noText}</span>;
  return <>{m.edited ? <><Pill>edit</Pill> </> : null}{m.long ? <details className="qm"><summary><span className="qm-t" dir="auto">{m.text}</span></summary><p className="pre" dir="auto">{m.text}</p></details> : <p className="pre" dir="auto">{m.text}</p>}</>;
}

export const Disp = ({ m }: { m: QaMsg }) => <>{m.disp ? <Pill tone={tone(m.disp.kind)}>{m.disp.text}</Pill> : null}{m.reason ? <p className="muted small" dir="auto">{m.reason}</p> : null}</>;

export function Chain({ c }: { c: Seg[][] }) {
  return c.length ? <ol className="q-chain">{c.map((s, i) => <li key={i}><Segs s={s} /></li>)}</ol> : <span className="muted">-</span>;
}

export function Sender({ m }: { m: QaMsg }) {
  return <><b dir="auto">{m.sender}</b><span className="mono small muted q-id" title={m.mxid}>{m.mxid}</span></>;
}

import { HelpQ } from "@/components/HelpQ";
import type { Metadata } from "next";
import { notFound } from "next/navigation";
import { EnvPill, Pill } from "@/components/Pills";
import { SevPill } from "@/components/Severity";
import { Button, buttonVariants } from "@/components/ui/button";
import { TitledCard } from "@/components/ui/card";
import { Banners, Crumb, FixCards, Hidden, Jump, pick, ProblemBox, Rail, Refresh, Report, type Search, Segs, Summary, Timeline, tone, When } from "@/components/Section";
import { Banner } from "@/components/Banner";
import { qs } from "@/lib/api";
import { full, plural } from "@/lib/format";
import { apiGet, csrfToken, readFlash } from "@/lib/server";
import type { Incident } from "@/lib/types";
import { incidentAct, incidentDeep } from "../actions";
import { notConfigured } from "@/components/NotConfigured";

export const metadata: Metadata = { title: "Incident" };

const WORDS: Record<string, string> = { ack: "Acknowledge", resolve: "Resolve", reopen: "Re-open" };

function Source({ s }: { s: Incident["source"] }) {
  const first = s.messages.slice(0, 3);
  const rest = s.messages.slice(3);
  const quote = (m: Incident["source"]["messages"][number], i: number) => (
    <li key={i}>
      <blockquote dir="auto">{m.body || <span className="muted">(no text)</span>}</blockquote>
      <p className="q-meta"><b dir="auto">{m.sender}</b> <span className="mono" title={m.mxid}>{m.mxid}</span> <Segs s={m.meta} /> · {m.disposition}</p>
    </li>
  );
  return (
    <TitledCard id="source" titleId="src-h" title={<>Source {s.messages.length ? <span className="ncount">{s.messages.length}</span> : null}</>}>
      <p className="tags">{s.tags.map((t) => <Pill key={t.value}>{t.label}</Pill>)}</p>
      {s.note ? <p className="card-sub">{s.note}</p> : null}
      {s.lines.length ? <ul className="plain">{s.lines.map((l, i) => <li key={i} dir="auto">{l}</li>)}</ul> : null}
      {first.length ? <ul className="quotes">{first.map(quote)}</ul> : null}
      {rest.length ? <details className="fold"><summary>{rest.length} more messages</summary><ul className="quotes">{rest.map(quote)}</ul></details> : null}
    </TitledCard>
  );
}

export default async function IncidentPage({ searchParams }: { searchParams: Search }) {
  const nc = await notConfigured("/incidents/view", "Incident");
  if (nc) return nc;
  const params = await pick(searchParams, ["id", "sent", "preq", "pq"]);
  const [r, csrf, flash] = await Promise.all([apiGet<Incident>("/api/v1/incidents/item" + qs(params)), csrfToken(), readFlash()]);
  if (r.status === 404) notFound();
  if (r.status !== 200) throw new Error(`/api/v1/incidents/item answered ${r.status}`);
  const d = r.data;
  const primaryAck = d.actions.includes("ack");
  const jump = [
    d.lazy ? { value: "lazy-reading", label: "Summary" } : null, d.problem ? { value: "problem", label: "Problem" } : null, { value: "source", label: "Source" },
    { value: "rca-reports", label: `RCA reports (${d.reports.length})` }, d.fix ? { value: "how-to-fix", label: "How to fix it" } : null,
    d.fix ? { value: "fix-prompt", label: "Fix prompt" } : null, { value: "timeline", label: `Timeline (${d.timeline.length})` },
    d.rca ? { value: "latest-rca", label: "Latest RCA" } : null, { value: "record", label: "Record" },
  ].filter((x) => x !== null);
  return (
    <>
      <Refresh on={d.refresh} />
      <header className="phead">
        <div>
          <Crumb href="/incidents" label="Incidents" />
          <h1 dir="auto">{d.title}</h1><HelpQ p="/incidents" />
          <p className="meta-line">
            <Pill tone={tone(d.statusKind)}>{d.status}</Pill>{d.env ? <EnvPill env={d.env} /> : null}<span>#{d.id}</span>
            {d.severity ? <SevPill sev={d.sevKey} word={d.severity} /> : null}<span>{d.owner ? `owner ${d.owner}` : "no owner"}</span>
            <span>{plural(d.occurrences, "finding")}</span><span>last <When ts={d.lastSeen} /></span>
          </p>
        </div>
      </header>
      <div className="actionbar">
        {d.actions.map((a) => (
          <form key={a} action={incidentAct}>
            <Hidden csrf={csrf} id={d.id} action={a} />
            {a === "resolve" ? <><label className="sr" htmlFor="res-note">Resolution note</label><input id="res-note" name="note" maxLength={300} placeholder="Cause or fix (optional)" autoComplete="off" /></> : null}
            <Button variant={a === "ack" ? "default" : "outline"}>{WORDS[a]}</Button>
          </form>
        ))}
        {d.task.href ? <a data-slot="button" className={buttonVariants({ variant: "outline" })} href={d.task.href}>{d.task.text}</a> : null}
        {!d.admin && d.status !== "resolved" ? <span className="muted small">Resolving is for admins; incidents also resolve on their own when the alerts stay clear.</span> : null}
      </div>
      <Banners list={d.banners} flash={flash} />
      {d.prod ? <Banner b={{ kind: "info", text: "Production: every fix step needs the operator's explicit approval. Argus never changes production." }} /> : null}
      <Jump items={jump} />
      <div className="stack">
        <Summary l={d.lazy} />
        <ProblemBox p={d.problem} csrf={csrf} />
        <Source s={d.source} />
      </div>
      <div className="detail">
        <div className="stack">
          <TitledCard id="rca-reports" titleId="rr-h" title={<>RCA reports <span className="ncount">{d.reports.length}</span></>}
            action={d.deep?.can ? <form action={incidentDeep}><Hidden csrf={csrf} id={d.id} /><Button variant={primaryAck ? "outline" : "default"} size="sm">Investigate deeply</Button></form> : null}>
            {d.deep?.can ? <p className="card-sub">Read-only deep investigation of edges, backends, code, history and deploys: about 30 min, up to $4.</p> : null}
            {d.deep?.message ? <p className="card-sub">{d.deep.message}</p> : null}
            {d.reports.length ? (
              <ul className="plain">{d.reports.map((x) => <li key={x.id}><a href={x.href} dir="auto">{x.title}</a> <Pill tone={tone(x.statusKind)}>{x.status}</Pill> <span className="muted small">{full(x.at)} · {x.source}{x.auto ? " · auto" : ""}</span></li>)}</ul>
            ) : <p className="empty">No RCA report for this incident yet.</p>}
          </TitledCard>
          {d.fix ? <FixCards f={d.fix} quiet={primaryAck} /> : null}
          <Timeline list={d.timeline} />
          {d.rca ? (
            <TitledCard id="latest-rca" titleId="lr-h" title={<>Latest RCA <span className="muted">run {d.rca.run}</span></>}>
              <p className="card-sub" dir="auto">{d.rca.title}</p>
              <Report doc={d.rca} folded={d.rca.folded} />
            </TitledCard>
          ) : null}
        </div>
        <aside><Rail facts={d.facts} id="record" /></aside>
      </div>
    </>
  );
}

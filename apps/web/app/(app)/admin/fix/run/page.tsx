import { HelpQ } from "@/components/HelpQ";
import type { Metadata } from "next";
import { notFound } from "next/navigation";
import { AdminsOnly, CellTable, OperatorSteps } from "@/components/Agent";
import { Reload } from "@/components/Infra";
import { Markdown } from "@/components/Markdown";
import { EnvPill, KindTag, Pill } from "@/components/Pills";
import { Banners, Crumb, pick, type Search, tone } from "@/components/Section";
import { qs } from "@/lib/api";
import { full } from "@/lib/format";
import { apiGet } from "@/lib/server";
import type { FixRun } from "@/lib/types";
import { PlainCard } from "@/components/ui/card";

export const metadata: Metadata = { title: "Fix run" };

export default async function FixRunPage({ searchParams }: { searchParams: Search }) {
  const r = await apiGet<FixRun>("/api/v1/fix/run" + qs(await pick(searchParams, ["id"])));
  if (r.status === 403) return <><header className="phead"><div><h1>Fix run</h1><HelpQ p="/admin/fix" /></div></header><AdminsOnly /></>;
  if (r.status === 404) notFound();
  if (r.status !== 200) throw new Error(`/api/v1/fix/run answered ${r.status}`);
  const d = r.data;
  const p = d.report;
  const who = <>{d.env ? <EnvPill env={d.env} /> : null}<span className="mono small">{d.hosts.join(" ")}</span><span>by {d.by}</span></>;
  return (
    <>
      <Reload r={d.refresh} />
      <header className="phead"><div><Crumb href="/admin/fix" label="Fix runs" /><h1>Fix run {d.id}</h1>
        <p className="meta-line">{p ? <><Pill tone={tone(p.status.kind)}>{p.status.text}</Pill><KindTag>{p.outcome}</KindTag></> : null}{who}
          {p ? <span>{full(p.at)} · {p.cost} · {p.turns} turns{p.denied !== "0" ? ` · ${p.denied} denied` : ""}</span> : null}</p></div></header>
      {d.wait ? (
        <div className="stack">
          <Banners list={[{ text: d.wait.detail ? `${d.wait.text} ${d.wait.detail}` : d.wait.text, kind: d.wait.kind === "busy" ? "info" : "fail" }]} />
          <OperatorSteps steps={d.wait.steps} />
        </div>
      ) : null}
      {p ? (
        <div className="stack">
          {p.problem ? <p className="small">Problem: <a href={p.problem.href} dir="auto">{p.problem.text}</a></p> : null}
          <p className="fx-lead" dir="auto">{p.summary}</p>
          <PlainCard as="section" aria-labelledby="rb-h"><div className="card-h"><h2 id="rb-h">Rollback commands</h2></div>
            {p.rollback ? <><p className="card-sub">Run by a human if the change must be undone.</p><pre className="code prompt" role="region" aria-label="Rollback commands" tabIndex={0}><code>{p.rollback}</code></pre></> : <p className="empty">The report lists no rollback commands.</p>}</PlainCard>
          <CellTable title="Actions" id="ac-h" head={["Host", "Command", "Result"]} rows={p.actions} />
          <CellTable title="Backups" id="bk-h" head={["Host", "Path"]} rows={p.backups} />
          <CellTable title="Verification" id="vf-h" head={["Host", "Check", "Result"]} rows={p.verification} />
          <PlainCard as="section" aria-labelledby="fr-h"><div className="card-h"><h2 id="fr-h">Final report</h2></div><div className="md"><Markdown text={p.md} /></div></PlainCard>
          <details className="fold"><summary>Exact prompt the fixer got <span className="muted">sha256 {p.promptSha}…</span></summary><pre className="code prompt" role="region" aria-label="Exact prompt" tabIndex={0}><code>{p.prompt}</code></pre></details>
        </div>
      ) : null}
    </>
  );
}

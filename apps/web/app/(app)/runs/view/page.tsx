import { HelpQ } from "@/components/HelpQ";
import type { Metadata } from "next";
import { notFound } from "next/navigation";
import { EnvPill, Pill } from "@/components/Pills";
import { Crumb, pick, Rail, Report, type Search, tone, When } from "@/components/Section";
import { Chain, Disp, MsgBody, Sender } from "@/components/Work";
import { qs } from "@/lib/api";
import { full, plural } from "@/lib/format";
import { apiGet } from "@/lib/server";
import type { Run } from "@/lib/types";
import { PlainCard } from "@/components/ui/card";

export const metadata: Metadata = { title: "Run" };

export default async function RunPage({ searchParams }: { searchParams: Search }) {
  const r = await apiGet<Run>("/api/v1/runs/item" + qs(await pick(searchParams, ["id"])));
  if (r.status === 404) notFound();
  if (r.status !== 200) throw new Error(`/api/v1/runs/item answered ${r.status}`);
  const d = r.data;
  return (
    <>
      <header className="phead"><div><Crumb href="/runs" label="Agent runs" /><h1>{d.task} · run {d.id}</h1><HelpQ p="/runs" />
        <p className="meta-line">{d.status ? <Pill tone={tone(d.status.kind)}>{d.status.text}</Pill> : null}<span>{full(d.at)}</span>{d.messages.length ? <span>· {d.messages.length} {plural(d.messages.length, "message")}</span> : null}</p></div></header>
      <div className="detail">
        <div className="stack">
          <PlainCard as="section" aria-labelledby="sum-h"><div className="card-h"><h2 id="sum-h">Summary</h2></div><Report doc={d.md} /></PlainCard>
          {d.showMessages ? (
            <PlainCard as="section" className="l-card" id="messages" aria-labelledby="msg-h"><div className="card-h pad"><h2 id="msg-h">Messages handled <span className="ncount">{d.messages.length}</span></h2></div>
              {d.messages.length ? (
                <div className="tablewrap" role="region" aria-label="Messages table" tabIndex={0}><table className="l-table q-t"><thead><tr><th scope="col">Time</th><th scope="col">Sender</th><th scope="col">Message</th><th scope="col">Disposition</th><th scope="col">Actions taken</th></tr></thead>
                  <tbody>{d.messages.map((m) => (
                    <tr key={m.id}><td data-label="Time" className="nw"><When ts={m.at} /><p className="muted small">{m.source}</p></td><td data-label="Sender"><Sender m={m} /></td>
                      <td className="t-main"><MsgBody m={m} />{m.link ? <p className="muted small"><a href={m.link} rel="noopener noreferrer">Open in Matrix</a></p> : null}</td>
                      <td data-label="Disposition"><Disp m={m} /></td><td data-label="Actions"><Chain c={m.chain ?? []} /></td></tr>
                  ))}</tbody></table></div>
              ) : <p className="empty pad">No message is recorded for this run (it ran before message tracking began and its fetch file is gone, or it had nothing new).</p>}
            </PlainCard>
          ) : null}
          {d.findings.length ? (
            <PlainCard as="section" className="l-card" aria-labelledby="fd-h"><div className="card-h pad"><h2 id="fd-h">{d.findingsWord} <span className="ncount">{d.findings.length}</span></h2></div>
              <ul className="rows">{d.findings.map((f) => (
                <li key={f.href}><p>{f.severity ? <><Pill tone={tone(f.severity.kind)}>{f.severity.text}</Pill> </> : null}<a href={f.href} dir="auto">{f.title}</a>{f.env ? <> <EnvPill env={f.env} /></> : null}</p><p className="muted small" dir="auto">{f.summary}</p></li>
              ))}</ul></PlainCard>
          ) : null}
        </div>
        <aside>
          <Rail facts={d.rail} />
          <PlainCard as="section" className="rail-c"><h2>Incidents</h2>{d.incidents.length ? <ul className="plain">{d.incidents.map((i) => <li key={i.href}><a href={i.href} dir="auto">{i.text}</a></li>)}</ul> : <p className="muted">None.</p>}</PlainCard>
        </aside>
      </div>
    </>
  );
}

import { HelpQ } from "@/components/HelpQ";
import type { Metadata } from "next";
import { AdminsOnly, OperatorSteps } from "@/components/Agent";
import { EnvPill, Pill } from "@/components/Pills";
import { Banners, tone, When } from "@/components/Section";
import { apiGet } from "@/lib/server";
import type { FixList } from "@/lib/types";
import { PlainCard } from "@/components/ui/card";
import { notConfigured } from "@/components/NotConfigured";

export const metadata: Metadata = { title: "Fix runs" };

export default async function FixRunsPage() {
  const nc = await notConfigured("/admin/fix", "Fix runs");
  if (nc) return nc;
  const r = await apiGet<FixList>("/api/v1/fix");
  if (r.status === 403) return <><header className="phead"><div><h1>Fix runs</h1><HelpQ p="/admin/fix" /></div></header><AdminsOnly /></>;
  if (r.status !== 200) throw new Error(`/api/v1/fix answered ${r.status}`);
  const d = r.data;
  return (
    <>
      <header className="phead"><div><h1>Fix runs</h1><HelpQ p="/admin/fix" /><p className="meta-line">Fixer {d.state} · scope: {d.envs}</p></div></header>
      <Banners list={d.notices} />
      <div className="stack">
        <OperatorSteps steps={d.steps} />
        <PlainCard as="section" className="l-card" aria-label="Fix runs">
          {d.rows.length ? (
            <div className="tablewrap" role="region" aria-label="Fix runs table" tabIndex={0}><table className="l-table"><thead><tr><th scope="col">Started</th><th scope="col">Env</th><th scope="col">Status</th><th scope="col">Summary</th><th scope="col">By</th></tr></thead>
              <tbody>{d.rows.map((x) => (
                <tr key={x.id}><td data-label="Started" className="nw"><When ts={x.at} /></td><td data-label="Env">{x.env ? <EnvPill env={x.env} /> : <span className="muted">-</span>}</td>
                  <td data-label="Status">{x.status ? <Pill tone={tone(x.status.kind)}>{x.status.text}</Pill> : null}</td>
                  <td className="t-main"><a href={`/admin/fix/run?id=${encodeURIComponent(x.id)}`} dir="auto">{x.summary || x.id}</a></td><td data-label="By" className="muted">{x.by}</td></tr>
              ))}</tbody></table></div>
          ) : <p className="empty pad">No fix run yet. A fix run starts from an incident on develop or pre-release: open its finding and choose Take action.</p>}
        </PlainCard>
      </div>
    </>
  );
}

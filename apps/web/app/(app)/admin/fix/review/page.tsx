import { HelpQ } from "@/components/HelpQ";
import type { Metadata } from "next";
import { notFound } from "next/navigation";
import { AdminsOnly, OperatorSteps } from "@/components/Agent";
import { FixReviewForm } from "@/components/FixReviewForm";
import { Markdown } from "@/components/Markdown";
import { EnvPill } from "@/components/Pills";
import { Banners, Crumb, pick, type Search } from "@/components/Section";
import { qs } from "@/lib/api";
import { apiGet, csrfToken } from "@/lib/server";
import type { FixReview } from "@/lib/types";
import { notConfigured } from "@/components/NotConfigured";

export const metadata: Metadata = { title: "Take action" };

export default async function FixReviewPage({ searchParams }: { searchParams: Search }) {
  const nc = await notConfigured("/admin/fix/review", "Take action");
  if (nc) return nc;
  const [r, csrf] = await Promise.all([apiGet<FixReview>("/api/v1/fix/review" + qs(await pick(searchParams, ["run", "idx", "rca"]))), csrfToken()]);
  if (r.status === 403) return <><header className="phead"><div><h1>Take action</h1><HelpQ p="/admin/fix" /></div></header><AdminsOnly /></>;
  if (r.status === 404) notFound();
  if (r.status !== 200) throw new Error(`/api/v1/fix/review answered ${r.status}`);
  const d = r.data;
  return (
    <>
      <header className="phead"><div><Crumb href={d.back.href} label={d.back.text} /><h1>Take action</h1><p className="meta-line">{d.env ? <EnvPill env={d.env} /> : null}<span dir="auto">{d.title}</span></p></div></header>
      <Banners list={d.notices} />
      <div className="stack">
        <OperatorSteps steps={d.steps} />
        {d.ready ? (
          <>
            <p className="fx-lead">Review and edit the fix prompt, pick the hosts, then confirm. The fixer runs as <code>argus-fix</code> on {d.env} only; every sudo command is logged on the host.</p>
            {d.preamble ? <details className="fold"><summary>Fixed preamble Argus puts before your prompt</summary><div className="md"><Markdown text={d.preamble} /></div></details>
              : <p className="muted small">Argus wraps your prompt in fixed rules: re-verify read-only first, plan, back up every file, one host at a time, verify, roll back on failure, stop on doubt, never production, structured report.</p>}
            <FixReviewForm d={d} csrf={csrf} />
          </>
        ) : null}
      </div>
    </>
  );
}

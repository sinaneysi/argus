"use client";

import { useActionState } from "react";
import { Banner } from "./Banner";
import { runFix, type FixState } from "@/app/(app)/admin/fix/actions";
import type { FixReview } from "@/lib/types";
import { Button } from "@/components/ui/button";
import { PlainCard } from "@/components/ui/card";

/** The one client part of Take action: a refused fix re-renders with the edited prompt and the hosts. Without JavaScript the form still posts. */
export function FixReviewForm({ d, csrf }: { d: FixReview; csrf: string }) {
  const q = new URLSearchParams(d.source).toString();
  const [state, action, busy] = useActionState<FixState | null, FormData>(runFix, null, "/admin/fix/review?" + q);
  const prompt = state?.prompt ?? d.prompt ?? "";
  const chosen = state?.hosts ?? [];
  return (
    <PlainCard as="form" action={action} className="fx-form">
      <input type="hidden" name="csrf" value={csrf} />
      {Object.entries(d.source).map(([k, v]) => <input key={k} type="hidden" name={k} value={v} />)}
      <fieldset className="fx-hosts" key={chosen.join(" ")}><legend>Target hosts</legend>
        {(d.hosts ?? []).map((h) => <label key={h} className="check"><input type="checkbox" name="host" value={h} defaultChecked={chosen.includes(h)} /><span className="mono">{h}</span></label>)}
      </fieldset>
      <label className="field"><span>Fix prompt</span><small>Up to {Math.floor((d.maxBytes ?? 0) / 1024)} KB. It was written from alerts, logs and chat; read it fully.</small>
        <textarea key={prompt.length} name="prompt" className="mono" rows={20} maxLength={d.maxBytes} defaultValue={prompt} required /></label>
      {state ? <Banner b={{ text: state.error, kind: state.kind }} /> : null}
      <div className="fx-foot">
        <label className="check"><input type="checkbox" name="reviewed" value="1" required /><span>I reviewed this prompt and the target hosts</span></label>
        <Button  disabled={busy}>{busy ? "Starting…" : "Run fix"}</Button>
        <span className="muted small">One fix at a time, at most 10 a day. Production is never touched.</span>
      </div>
    </PlainCard>
  );
}

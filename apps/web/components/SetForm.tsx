"use client";

import { useActionState } from "react";
import { Banner } from "./Banner";
import { Icon } from "./Icon";
import { settingsAct, type SetState } from "@/app/(app)/admin/settings";
import { Button } from "@/components/ui/button";

export type SetField = {
  name: string; label: React.ReactNode; type?: "text" | "url" | "password" | "number" | "check" | "select" | "area" | "radio" | "checks" | "file";
  value?: string; on?: boolean; options?: { value: string; label: string; sub?: string; disabled?: boolean }[]; hint?: React.ReactNode; range?: string;
  min?: number; max?: number; minLength?: number; maxLength?: number; pattern?: string; required?: boolean; placeholder?: string;
  autoComplete?: string; rows?: number; mono?: boolean; fold?: string; foldOpen?: boolean; accept?: string; wide?: boolean;
};

const fid = (form: string, n: string) => `f-${form}-${n.replace(/[:_]/g, "-")}`;

function Input({ f, id, st, bad, hintId }: { f: SetField; id: string; st: SetState | null; bad: boolean; hintId?: string }) {
  const desc = [bad ? `${id}-err` : "", hintId ?? ""].filter(Boolean).join(" ") || undefined;
  // a refusal remounts the form: focus goes to the marked field, not to the page
  const a = { id, name: f.name, "aria-invalid": bad || undefined, "aria-describedby": desc, autoFocus: bad || undefined };
  const v = st ? st.values[f.name] ?? "" : f.value ?? "";
  const text = { maxLength: f.maxLength, minLength: f.minLength, pattern: f.pattern, required: f.required, placeholder: f.placeholder, spellCheck: false };
  switch (f.type) {
    case "check": return <input type="checkbox" value={f.value ?? "1"} defaultChecked={st ? st.values[f.name] === (f.value ?? "1") : f.on} {...a} />;
    case "number": return <input type="number" min={f.min} max={f.max} required={f.required} defaultValue={v} {...a} />;
    case "select": return <select defaultValue={v} {...a}>{f.options?.map((o) => <option key={o.value} value={o.value}>{o.label}</option>)}</select>;
    case "area": return <textarea rows={f.rows ?? 3} className={f.mono === false ? "set-area" : "set-area mono-area"} defaultValue={v} {...text} {...a} />;
    // a secret is never filled back in, not even after a refused save
    case "password": return <input type="password" autoComplete={f.autoComplete ?? "off"} {...text} {...a} />;
    case "file": return <input type="file" accept={f.accept} required={f.required} {...a} />;
    default: return <input type={f.type ?? "text"} defaultValue={v} {...text} {...a} />;
  }
}

/** Radio buttons, or with type "checks" a group of checkboxes that post one entry each (their value is comma-joined). */
function Radio({ f, id, st, bad, hint, err }: { f: SetField; id: string; st: SetState | null; bad: boolean; hint: React.ReactNode; err: React.ReactNode }) {
  const cur = st ? st.values[f.name] ?? "" : f.value ?? "";
  const many = f.type === "checks";
  const on = (v: string) => (many ? cur.split(",").includes(v) : cur === v);
  const desc = [bad ? `${id}-err` : "", f.hint ? `${id}-hint` : ""].filter(Boolean).join(" ") || undefined;
  return (
    <fieldset className="set-opts" role={many ? "group" : "radiogroup"} aria-invalid={bad || undefined} aria-describedby={desc}><legend>{f.label}</legend>
      {f.options?.map((o) => <label key={o.value} className="set-opt"><input type={many ? "checkbox" : "radio"} name={f.name} value={o.value} defaultChecked={on(o.value)} disabled={o.disabled} /><span><b>{o.label}</b>{o.sub ? <small>{o.sub}</small> : null}</span></label>)}
      {hint}{err}
    </fieldset>
  );
}

function One({ f, form, stack, st }: { f: SetField; form: string; stack: boolean; st: SetState | null }) {
  const id = fid(form, f.name);
  const bad = st?.field === f.name;
  const hintId = f.hint ? `${id}-hint` : undefined;
  const err = bad ? <span className="set-err" id={`${id}-err`}><Icon name="fail" size={14} />{st?.error}</span> : null;
  const hint = f.hint ? <small id={hintId}>{f.hint}</small> : null;
  const input = <Input f={f} id={id} st={st} bad={bad} hintId={hintId} />;
  // a text area, a choice of options or a file never fits a row's narrow value column
  if (stack || f.type === "area" || f.type === "radio" || f.type === "checks" || f.type === "file") {
    const box = f.type === "radio" || f.type === "checks" ? <Radio f={f} id={id} st={st} bad={bad} hint={hint} err={err} />
      : f.type === "check" ? <div className="set-chk">{input}<label htmlFor={id}>{f.label}</label>{hint}{err}</div>
        : <div className="field"><label htmlFor={id}>{f.label}</label>{input}{hint}{err}</div>;
    return f.fold ? <details className="fold" open={bad || f.foldOpen}><summary>{f.fold}</summary>{box}</details> : box;
  }
  return (
    <div className={f.wide ? "set-row wide" : "set-row"}><div className="set-l"><label htmlFor={id}>{f.label}</label>{hint}</div>{input}
      <span className="set-range">{f.range ?? (f.type === "check" ? "on/off" : "")}</span>{err}</div>
  );
}

/** A Settings form: rows of label, input and range, or stacked fields. It posts through one action that names its route;
 * a refused save comes back with the entries kept, except secrets, and the field marked. With foldTitle it sits in a fold
 * that a refusal opens, so the answer is never hidden. Without JavaScript it still posts. */
export function SetForm({ to, page, csrf, form, hidden, who, fields, submit, submitName, secondary, note, stack, foldTitle, foldId, foldOpen, lead }: {
  to: string; page: string; csrf: string; form: string; hidden?: Record<string, string>; who?: string; fields: SetField[]; submit: string; submitName?: string;
  secondary?: boolean; note?: React.ReactNode; stack?: boolean; foldTitle?: React.ReactNode; foldId?: string; foldOpen?: boolean; lead?: React.ReactNode;
}) {
  // each form its own permalink: without JavaScript only the form that posted gets the answer
  const [path, hash] = page.split("#");
  const link = `${path}${path?.includes("?") ? "&" : "?"}f=${form}${hash ? `#${hash}` : ""}`;
  const [st, action, busy] = useActionState<SetState | null, FormData>(settingsAct, null, link);
  const body = (
    <form action={action} className={stack ? "set-form set-stack" : "set-form"} autoComplete="off" key={st ? JSON.stringify(st.values) : "stored"}>
      <input type="hidden" name="csrf" value={csrf} /><input type="hidden" name="_to" value={to} />
      {Object.entries(hidden ?? {}).map(([k, v]) => <input key={k} type="hidden" name={k} value={v} />)}
      {/* a password manager files a token under this user instead of the page's login; the action never posts it */}
      {who ? <input type="text" className="sr" name="who" value={who} autoComplete="username" readOnly tabIndex={-1} aria-hidden="true" /> : null}
      {st ? <Banner b={{ text: st.error, kind: st.kind }} /> : null}
      {stack ? fields.map((f) => <One key={f.name} f={f} form={form} stack st={st} />) : <div className="set-rows">{fields.map((f) => <One key={f.name} f={f} form={form} stack={false} st={st} />)}</div>}
      <p className="row-act"><Button variant={secondary ? "outline" : "default"} disabled={busy} aria-label={submitName}>{busy ? "Saving…" : submit}</Button>{note ? <span className="muted small">{note}</span> : null}</p>
    </form>
  );
  return foldTitle ? <details className="fold" id={foldId} open={!!st || foldOpen}><summary>{foldTitle}</summary>{lead}{body}</details> : body;
}

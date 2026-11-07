import { TitledCard } from "./ui/card";
import type { ConnectStep } from "@/lib/types";

function Steps({ steps }: { steps: ConnectStep[] }) {
  return (
    <ol className="mt-3 grid gap-3">
      {steps.map((s) => <li key={s.label}><p className="text-sm font-medium">{s.label}</p><pre className="code mt-1"><code>{s.cmd}</code></pre></li>)}
    </ol>
  );
}

/** The same steps as a fold inside a card that lists many resources. */
export function ConnectFold({ steps, via }: { steps?: ConnectStep[] | null; via?: string }) {
  if (!steps?.length) return null;
  return <details className="fold"><summary>Connect with {via}</summary><Steps steps={steps} /></details>;
}

/** How a person reaches this resource through the access broker. Argus shows the way; the broker decides who may enter. */
export function Connect({ id, steps, via }: { id: string; steps?: ConnectStep[] | null; via?: string }) {
  if (!steps?.length) return null;
  return (
    <TitledCard id={id} titleId={`${id}-h`} title={`Connect with ${via}`}>
      <p className="card-sub">Run these in your terminal. {via} decides what your account may reach and records the session.</p>
      <Steps steps={steps} />
    </TitledCard>
  );
}

import { PlainCard } from "@/components/ui/card";
import { qs } from "@/lib/api";
import { apiGet, shellMe } from "@/lib/server";
import type { NeedSource, Needs } from "@/lib/types";

// The shared "Connect X to enable this page" state (ADR-0021 D5): a page whose data source is not connected shows this
// instead of an empty or broken page. Built from the empty-state pattern (DESIGN.md 3.20): one card, the fact, the next
// step; the link only for admins (a viewer cannot connect a source).

const SETTINGS: Record<string, string> = { "/admin/claude": "Settings → Claude", "/admin/kube": "Settings → Kubernetes" };

function labels(src: NeedSource[]): string {
  return src.map((s) => s.label).join(", or ");
}

export function NotConfiguredCard({ sources, admin }: { sources: NeedSource[]; admin: boolean }) {
  const first = sources[0];
  const where = first ? SETTINGS[first.settings] ?? "Settings → Data sources" : "Settings → Data sources";
  return (
    <PlainCard as="section" data-state="not-configured" aria-label="Not configured">
      <p className="empty"><b>Connect {labels(sources)} to enable this page.</b>{" "}
        {admin && first ? <>Add it in <a href={first.settings}>{where}</a>; the page fills at the next run.</> : <>An admin connects it in {where}.</>}
      </p>
    </PlainCard>
  );
}

/** The not-configured page for `page` (its header and the card), or null when its sources are connected. Call it first. */
export async function notConfigured(page: string, title: string): Promise<React.ReactElement | null> {
  const r = await apiGet<Needs>("/api/v1/needs" + qs({ page }));
  const src = r.status === 200 ? r.data.notConfigured?.sources : undefined;
  if (!src?.length) return null;
  const me = await shellMe();
  return (
    <>
      <header className="phead"><div><h1>{title}</h1></div></header>
      <NotConfiguredCard sources={src} admin={me.role === "admin"} />
    </>
  );
}

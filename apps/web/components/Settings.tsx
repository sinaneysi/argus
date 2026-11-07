import "./section.css";
import "./settings.css";

const TABS: [string, string][] = [["General", "/admin"], ["Claude", "/admin/claude"], ["Data sources", "/admin/sources"], ["Kubernetes", "/admin/kube"], ["Users and access", "/admin/users"],
  ["Jira sync", "/admin/board"], ["Backup targets", "/admin/backup-targets"], ["Integrations", "/admin/outline"], ["Credentials", "/admin/credentials"]];

/** The Settings pages' tabs; the ones not ported yet are still Go pages on the same origin. */
export function SettingsTabs({ cur }: { cur: string }) {
  return <nav className="tabs" aria-label="Settings">{TABS.map(([l, h]) => <a key={h} href={h} className={h === cur ? "on" : undefined} aria-current={h === cur ? "page" : undefined}>{l}</a>)}</nav>;
}

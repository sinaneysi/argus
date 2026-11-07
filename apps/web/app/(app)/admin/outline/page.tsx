import type { Metadata } from "next";
import { AdminsOnly } from "@/components/Agent";
import { Pill } from "@/components/Pills";
import { Banners, Hidden, pick, Refresh, tone, type Search } from "@/components/Section";
import { SetForm } from "@/components/SetForm";
import { SettingsTabs } from "@/components/Settings";
import { qs } from "@/lib/api";
import { plural } from "@/lib/format";
import { apiGet, csrfToken, readFlash } from "@/lib/server";
import type { AdminOutline } from "@/lib/types";
import { settingsGo } from "../settings";
import { Button } from "@/components/ui/button";
import { PlainCard } from "@/components/ui/card";

export const metadata: Metadata = { title: "Integrations" };

const PAGE = "/admin/outline";
type Gitlab = AdminOutline["gitlab"];

function Facts({ rows }: { rows: [string, React.ReactNode][] }) {
  return <dl className="set-kv">{rows.map(([k, v]) => <div key={k}><dt>{k}</dt><dd>{v}</dd></div>)}</dl>;
}

function OutlineSteps({ d }: { d: AdminOutline }) {
  return (
    <ol className="set-steps">
      <li>In Outline (<span className="mono">{d.outline_host}</span>), sign in, open <b>Settings → API</b> (in newer versions <b>Settings → API &amp; Apps</b>) and choose <b>New API key</b>. Name it <span className="mono">argus</span>. Your own account works; the pages then show you as their author. Copy the key once: Outline shows it only now.</li>
      <li>Paste the key below, keep the collection <span className="mono">Services</span> (tick <b>Create it</b> the first time) and choose <b>Test and save</b>. Argus calls <span className="mono">auth.info</span> from {d.host}, shows the user and team, then finds or creates the collection.</li>
      <li>Read the previews below. When they look right, switch the mode to <b>Publish</b> and choose <b>Sync now</b>: Argus creates one page per service and the <b>Services overview</b> page, then refreshes them every morning.</li>
      <li>People may write anywhere on a page. Argus rewrites only the text between its <span className="mono">argus:begin</span> and <span className="mono">argus:end</span> markers; if someone edits inside them, Argus keeps the edit and adds an <b>Argus proposed update</b> section instead.</li>
    </ol>
  );
}

function OutlineConnect({ d, csrf, fold }: { d: AdminOutline; csrf: string; fold?: string }) {
  return (
    <SetForm to="outline" page={PAGE} csrf={csrf} form="outline-connect" stack hidden={{ action: "connect" }} who={d.user} submit="Test and save" submitName="Test and save Outline" foldTitle={fold} foldId={fold ? "outline-replace" : undefined}
      note={`Argus tests the key from ${d.host}; the result shows at the top of the page.`}
      fields={[
        { name: "url", label: "Outline URL", type: "url", value: d.url, maxLength: 300, required: true, hint: `Allowed Outline hosts: ${d.hosts.join(", ")}.` },
        { name: "token", label: "API key", type: "password", maxLength: 200, required: true, hint: `Outline: Settings → API → New API key. Sent once to ${d.host} and stored there for root only; Argus never shows it again, not even after a rejected save.` },
        { name: "collection", label: "Collection", value: d.collection, maxLength: 100, required: true },
        { name: "create_collection", label: "Create it", type: "check", hint: "Only if no collection has this name yet (the first time)." },
      ]} />
  );
}

function Pages({ d }: { d: AdminOutline }) {
  return (
    <div id="pages"><h3>Pages <span className="ncount">{d.pages.length}</span></h3>
      {d.pages.length ? <div className="tablewrap" role="region" aria-label="Built pages" tabIndex={0}><table className="l-table">
        <thead><tr><th scope="col">Page</th><th scope="col">Since last build</th><th scope="col">In Outline</th><th scope="col">Content changed</th><th scope="col">Note</th></tr></thead>
        <tbody>{d.pages.map((p) => (
          <tr key={p.service}>
            <td className="t-main"><a href={`/admin/outline/page?s=${encodeURIComponent(p.service)}`}>{p.name}</a></td>
            <td data-label="Since last build">{p.local.kind === "none" ? <span className="muted">{p.local.text}</span> : <Pill tone={tone(p.local.kind)}>{p.local.text}</Pill>}</td>
            <td data-label="In Outline">{p.state ? <Pill tone={tone(p.state.kind)}>{p.state.text}</Pill> : <span className="muted">not published</span>}{p.url ? <> <a href={p.url} rel="noopener noreferrer" aria-label={`Open ${p.name} in Outline`}>Open</a></> : null}</td>
            <td className="nw" data-label="Content changed">{p.changed}</td>
            <td className="small" data-label={p.note ? "Note" : undefined}>{p.note}</td>
          </tr>
        ))}</tbody>
      </table></div> : <p className="empty">No page built yet. Sync now builds the previews (argus-outline.service on {d.host}).</p>}
    </div>
  );
}

function GitlabConnect({ d, g, csrf, fold }: { d: AdminOutline; g: Gitlab; csrf: string; fold?: string }) {
  return (
    <SetForm to="gitlab" page={`${PAGE}#gitlab`} csrf={csrf} form="gitlab-connect" stack hidden={{ action: "connect" }} who={d.user} submit="Test and save" submitName="Test and save GitLab" foldTitle={fold}
      note={`Argus tests the token from ${d.host}; the result shows at the top of this section.`}
      fields={[
        { name: "url", label: "GitLab URL", type: "url", value: g.url, maxLength: 300, placeholder: "https://gitlab.example.com", hint: `Allowed GitLab hosts: ${g.hosts.length ? g.hosts.join(", ") : "none yet (set GITLAB_URL in /etc/argus/env, or add one under Allowed GitLab hosts)"}.` },
        { name: "project", label: "Issue board", value: g.project, maxLength: 400, required: true, placeholder: "paste the board URL (group or project), or group/project" },
        { name: "board", label: "Board (optional)", maxLength: 400, placeholder: "board number or URL; empty: the only board, or choose after saving" },
        { name: "token", label: "Access token (group or project)", type: "password", maxLength: 200, required: true, hint: `Role Reporter, scope api. Sent once to ${d.host} and stored there for root only; Argus never shows it again, not even after a rejected save.` },
      ]} />
  );
}

function GitlabFacts({ g }: { g: Gitlab }) {
  const c = g.connection!;
  const rows: [string, React.ReactNode][] = [["GitLab", <span key="u" className="mono small">{c.gitlab}</span>],
    [c.where_kind, c.web_url ? <a href={c.web_url} rel="noopener noreferrer">{c.where}</a> : c.where],
    ["Board", c.board_id ? <>{c.board} <span className="muted small">#{c.board_id}</span></> : <span className="muted">none chosen</span>]];
  if (g.group) rows.push(["Default project", c.default_project || <Pill tone="warn">none chosen</Pill>]);
  rows.push(["Token user", <>{c.token_user} {c.bot ? <span className="muted small">{g.group ? "group bot" : "project bot"}</span> : <Pill tone="warn">personal token</Pill>}</>],
    ["Since", <>{c.since} <span className="muted">by {c.by}</span></>]);
  return <Facts rows={rows} />;
}

function Gitlab({ d, csrf }: { d: AdminOutline; csrf: string }) {
  const g = d.gitlab;
  const page = `${PAGE}#gitlab`;
  return (
    <PlainCard as="section" id="gitlab" aria-labelledby="gitlab-h">
      <div className="card-h"><h2 id="gitlab-h">GitLab issues</h2><div className="u-acts"><Pill tone={g.connected ? "ok" : "neutral"}>{g.connected ? "Connected" : "Not connected"}</Pill>
        {g.connected ? <form action={settingsGo}><Hidden csrf={csrf} _to="gitlab" action="sync" /><Button variant="outline" size="sm" aria-label="Sync now: GitLab">Sync now</Button></form> : null}</div></div>
      <p className="set-note">A second task tracker beside Jira. Argus mirrors one issue board, two ways, every two minutes.</p>
      <Banners list={g.notices} />
      {g.connected ? <>
        <GitlabFacts g={g} />
        {g.projects?.length ? <SetForm to="gitlab" page={page} csrf={csrf} form="gitlab-project" hidden={{ action: "project" }} secondary submit="Use this project"
          fields={[{ name: "project", label: "Default project for new issues", type: "select", value: g.default_project, wide: true, options: [...(g.default_project ? [] : [{ value: "", label: "Choose a project" }]), ...g.projects] }]} /> : null}
        {g.boards?.length ? <SetForm to="gitlab" page={page} csrf={csrf} form="gitlab-board" hidden={{ action: "board" }} secondary submit="Use this board"
          fields={[{ name: "board", label: "Issue board", type: "select", value: g.connection?.board_id ? String(g.connection.board_id) : "", wide: true, options: [...(g.connection?.board_id ? [] : [{ value: "", label: "Choose a board" }]), ...g.boards] }]} /> : null}
        <details className="fold"><summary>Board lists → Argus columns</summary>
          <div className="tablewrap" role="region" aria-label="Board lists" tabIndex={0}><table className="l-table"><thead><tr><th scope="col">GitLab list</th><th scope="col">Argus column</th></tr></thead>
            <tbody>{g.lists?.map(([l, c]) => <tr key={l}><td className="t-main mono">{l}</td><td data-label="Argus column">{c}</td></tr>)}</tbody></table></div></details>
        <GitlabConnect d={d} g={g} csrf={csrf} fold="Replace the token or board" />
        <details className="fold"><summary aria-label="Disconnect GitLab">Disconnect…</summary><form action={settingsGo} className="u-confirm"><Hidden csrf={csrf} _to="gitlab" action="disconnect" />
          <Button variant="destructive" size="sm">Disconnect GitLab</Button><span className="muted small">Deletes the token on {d.host}; revoke it in GitLab too. GitLab tasks stay on the board, read-only.</span></form></details>
      </> : <>
        <details className="fold"><summary>Where to find the board URL and the token</summary><ol className="set-steps">
          <li>Open the issue board in GitLab and copy its URL. A <b>group board</b> (<span className="mono">…/groups/&lt;group&gt;/-/boards</span>) shows the issues of every project in the group; a project board shows only that project&apos;s.</li>
          <li>Create the token where the board lives: the group&apos;s (or the project&apos;s) <b>Settings → Access tokens</b>. Name <span className="mono">argus</span>, role <b>Reporter</b>, scope <b>api</b>, with an expiry date. It belongs to a bot user, so the integration keeps working when people leave. Do not use a personal token.</li>
          <li>Below, paste the board URL and the token and choose <b>Test and save</b>. For a group board, then pick the <b>default project</b> new issues go to; people can pick another one when they create a task.</li></ol></details>
        <GitlabConnect d={d} g={g} csrf={csrf} />
      </>}
      <h3>Defaults</h3>
      <SetForm to="gitlab" page={page} csrf={csrf} form="gitlab-settings" stack secondary hidden={{ action: "settings" }} submit="Save defaults" note="The board sync applies it; the result shows at the top of this section."
        fields={[
          { name: "default_tracker", label: "New tasks go to", type: "radio", value: g.default_tracker, options: [{ value: "jira", label: "Jira", sub: "The default. A person can still pick GitLab issues on New task and Accept." }, { value: "gitlab", label: "GitLab issues", sub: `Preselected on New task and Accept.${g.connected ? "" : " Connect first."}`, disabled: !g.connected }] },
          { name: "list_map", label: "List map", value: g.list_map, fold: "List map (optional)", foldOpen: !!g.list_map, maxLength: 600, placeholder: "QA=review; Blocked=progress", hint: "A list whose name Argus does not recognise counts as In progress. Columns: todo, progress, review, fixer, done." },
          { name: "user_map", label: "User map", value: g.user_map, fold: "User map (optional)", foldOpen: !!g.user_map, maxLength: 600, placeholder: "argus.user=gitlab.user, …", hint: "For Assign to me and assignees: an Argus user without a line is the GitLab user of the same name." },
        ]} />
      <SetForm to="gitlab" page={page} csrf={csrf} form="gitlab-hosts" foldTitle="Allowed GitLab hosts" foldId="gitlab-hosts" stack secondary hidden={{ action: "hosts" }} submit="Save GitLab hosts"
        lead={<p className="set-note">Argus calls only these hosts with the token (https, port 443 unless written as host:port; never loopback, link-local or metadata addresses; no redirects; only the project or group, its boards, projects, issues and their notes). Every change is in the audit log.</p>}
        fields={[{ name: "hosts", label: "Allowed GitLab hosts (one per line, host or host:port, at most 10)", type: "area", rows: 2, value: g.hosts.join("\n") }]} />
    </PlainCard>
  );
}

/** Settings → Integrations: Outline service documentation and GitLab issues. Keys go in once and never come back. */
export default async function OutlinePage({ searchParams }: { searchParams: Search }) {
  const q = await pick(searchParams, ["r", "g"]);
  const [r, csrf, flash] = await Promise.all([apiGet<AdminOutline>("/api/v1/admin/outline" + qs(q)), csrfToken(), readFlash()]);
  if (r.status === 403) return <><header className="phead"><div><h1>Integrations</h1></div></header><AdminsOnly /></>;
  if (r.status !== 200) throw new Error(`/api/v1/admin/outline answered ${r.status}`);
  const d = r.data;
  const c = d.connection;
  return (
    <>
      <Refresh on={!!d.refresh} />
      <header className="phead"><div><h1>Integrations</h1><p className="psub">Outline {d.connected ? "connected" : "not connected"} · <b>{d.mode === "publish" ? "Publish" : "Preview"}</b> mode · {plural(d.built, "service page")} built · Both are listed in <a href="/admin/sources">Data sources</a></p></div></header>
      <SettingsTabs cur={PAGE} />
      <div className="stack">
        <nav className="jump" aria-label="Integrations"><a href="#outline">Outline</a><a href="#gitlab">GitLab issues</a></nav>
        <Banners list={d.notices} flash={flash} />
        <PlainCard as="section" id="outline" aria-labelledby="outline-h">
          <div className="card-h"><h2 id="outline-h">Outline: service documentation</h2><form action={settingsGo}><Hidden csrf={csrf} _to="outline" action="sync" /><Button variant="outline" size="sm" aria-label="Sync now: Outline">Sync now</Button></form></div>
          <p className="set-note">One page per service in Outline, built by Argus from the context service files, Service-info.md, CI/CD and incidents, plus a Services overview. Daily at 07:40 and on Sync now.</p>
          {c ? <>
            <Facts rows={[["Outline", <span key="u" className="mono small">{c.url}</span>], ["Connected as", <>{c.user} <span className="muted small">team {c.team}</span></>], ["Collection", c.collection], ["Since", <>{c.since} <span className="muted">by {c.by}</span></>]]} />
            <OutlineConnect d={d} csrf={csrf} fold="Replace the API key, URL or collection" />
            <details className="fold"><summary aria-label="Disconnect Outline">Disconnect…</summary><form action={settingsGo} className="u-confirm"><Hidden csrf={csrf} _to="outline" action="disconnect" />
              <Button variant="destructive" size="sm">Disconnect Outline</Button><span className="muted small">Deletes the API key on {d.host}, so nothing more reaches Outline. Revoke the key in Outline too.</span></form></details>
          </> : <><OutlineSteps d={d} /><OutlineConnect d={d} csrf={csrf} /></>}
          <h3>Mode and scope</h3>
          <SetForm to="outline" page={PAGE} csrf={csrf} form="outline-settings" stack secondary hidden={{ action: "settings" }} submit="Save mode and scope" note="Takes effect at the next run (daily 07:40, or Sync now)."
            fields={[
              { name: "mode", label: "Mode", type: "radio", value: d.mode, options: [{ value: "preview", label: "Preview", sub: "Pages are built here only; nothing is sent to Outline." }, { value: "publish", label: "Publish", sub: `Create and update the pages in Outline daily and on Sync now.${d.connected ? "" : " Connect first."}`, disabled: !d.connected }] },
              { name: "scope", label: "Services", type: "radio", value: d.scope, options: [{ value: "pilot", label: "Pilot (5)", sub: "payment, shop-front-ts, sms-service, warehouse/api, wallet-ts" }, { value: "all", label: "All services", sub: "Every service in the context files (about 125)." }] },
            ]} />
          <Pages d={d} />
          <SetForm to="outline" page={PAGE} csrf={csrf} form="outline-hosts" foldTitle="Allowed Outline hosts" foldId="hosts" stack secondary hidden={{ action: "hosts" }} submit="Save Outline hosts"
            lead={<p className="set-note">Argus calls only these hosts with the API key (https, port 443 unless written as host:port; never loopback, link-local or metadata addresses; no redirects; only eight read and write methods of the documents and collections API, nothing that deletes). Every change is in the audit log.</p>}
            fields={[{ name: "hosts", label: "Allowed Outline hosts (one per line, host or host:port, at most 10)", type: "area", rows: 2, value: d.hosts.join("\n") }]} />
          {d.connected ? <details className="fold"><summary>How it works</summary><OutlineSteps d={d} /></details> : null}
        </PlainCard>
        <Gitlab d={d} csrf={csrf} />
      </div>
    </>
  );
}

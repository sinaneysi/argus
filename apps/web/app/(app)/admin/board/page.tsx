import type { Metadata } from "next";
import { AdminsOnly } from "@/components/Agent";
import { Banners, Hidden, pick, Rail, Refresh, When, type Search } from "@/components/Section";
import { settingsGo } from "../settings";
import { SetForm, type SetField } from "@/components/SetForm";
import { SettingsTabs } from "@/components/Settings";
import { HelpQ } from "@/components/HelpQ";
import { qs } from "@/lib/api";
import { apiGet, csrfToken, readFlash } from "@/lib/server";
import type { AdminBoard } from "@/lib/types";
import { Button } from "@/components/ui/button";
import { PlainCard } from "@/components/ui/card";

export const metadata: Metadata = { title: "Jira sync" };

const PAGE = "/admin/board";
const applied = "The board sync applies it; the result shows at the top of the page.";

const Steps = ({ host }: { host: string }) => (
  <ol className="set-steps">
    <li>In Jira, open Profile → Personal Access Tokens and create a token. Your own account works; a separate bot account is optional (then tasks show the bot as the author of Argus&apos;s changes).</li>
    <li>Paste the Jira URL and the token below and choose Test and save. Argus checks the token from {host}, lists the projects the token can see, and you pick the project for new tasks.</li>
    <li>&quot;Ready for fixer&quot; uses a Jira status of that name when the workflow has one; otherwise Argus uses the label <code>argus-fixer</code> on the issue, so no Jira admin change is needed.</li>
    <li>Everyone else can connect their own Jira in Account; their own token is then used for their own changes.</li>
  </ol>
);

function connectFields(d: AdminBoard): SetField[] {
  const m = d.shared;
  return [
    { name: "url", label: "Jira URL", type: "url", value: m?.url ?? "", placeholder: "https://jira.example.com", maxLength: 300, required: true },
    { name: "token", label: "Personal access token", type: "password", minLength: 10, maxLength: 512, required: true,
      hint: `Stored on ${d.host} for root only (the board sync); this page never shows it again, not even after a rejected save. Saving a new host adds it to Allowed Jira hosts.` },
    { name: "project", label: "Project key (optional now)", value: m?.project ?? "", maxLength: 20, pattern: "[A-Za-z][A-Za-z0-9_]{0,19}", placeholder: "pick after the test" },
    { name: "board_id", label: "Board (optional: id or board URL)", value: m?.boardId ?? "", maxLength: 300, placeholder: "11 or https://…/boards/11" },
    { name: "approvers", label: "Fixer approvers in Jira (optional)", value: m?.approvers ?? "", maxLength: 300, placeholder: "jira.user1, jira.user2" },
    { name: "mode", label: "Mode", type: "radio", value: m?.mode ?? "ro", options: [
      { value: "ro", label: "Read-only", sub: "Pull issues; changes from Argus need a personal read and write connection." },
      { value: "rw", label: "Read and write", sub: "Argus writes creates, moves, comments with this token when the user has no own connection." }] },
    { name: "ca_pem", label: "CA certificate", type: "area", rows: 6, placeholder: "-----BEGIN CERTIFICATE-----", fold: "Internal CA certificate (optional)",
      hint: "Only if Jira uses a certificate from an internal CA. Paste PEM certificates only; root checks that they parse." },
  ];
}

function sharedFields(s: NonNullable<AdminBoard["shared"]>): SetField[] {
  const out: SetField[] = [{ name: "project", label: "Project for new tasks", type: "select", value: s.project, options: [{ value: "", label: "choose" }, ...s.projects] }];
  if (s.boards.length) out.push({ name: "board_pick", label: "Board", type: "select", value: s.boardId, options: [{ value: "", label: "none" }, ...s.boards] });
  out.push({ name: "board_id", label: `${s.boards.length ? "Or paste a board" : "Board"} (id or board URL)`, value: s.boards.length ? "" : s.boardId, maxLength: 300, placeholder: "11 or https://…/boards/11" },
    { name: "mode", label: "Mode", type: "select", value: s.mode, options: [{ value: "ro", label: "Read-only" }, { value: "rw", label: "Read and write" }] },
    { name: "approvers", label: "Fixer approvers in Jira", value: s.approvers, maxLength: 300, placeholder: "optional" });
  if (s.components.length) out.push({ name: "component", label: "Component for new issues", type: "select", value: s.component, options: [{ value: "", label: "none" }, ...s.components.map((c) => ({ value: c, label: c }))] });
  return out;
}

function agentFields(d: AdminBoard): SetField[] {
  const s = d.agent.settings;
  const rw = d.agent.rwUsers.join(", ") || "none yet";
  return [
    { name: "enabled", label: "Argus works In Progress tasks", type: "check", on: s.enabled },
    { name: "assignees", label: "Works for these Argus admins", wide: true, value: s.assignees, maxLength: 400, range: "names, comma separated", hint: `Empty: the admins with a read-and-write connection (${rw}).` },
    { name: "column", label: "Agent column of the board", value: s.column, maxLength: 60, range: "text" },
    { name: "auto_done", label: "Move to Done when a run succeeds", type: "check", on: s.autoDone },
    { name: "qa_enabled", label: "QA and monitoring reports become proposed tasks", type: "check", on: s.qaEnabled, hint: <>An admin accepts them in <a href="/review?kind=proposals">Review</a>.</> },
    { name: "qa_threshold", label: "QA intake severity", type: "select", value: s.qaThreshold, range: "choice", hint: "Production always.", options: ["critical", "high", "medium"].map((v) => ({ value: v, label: `${v} and above` })) },
    { name: "qa_cap", label: "New QA proposals per day at most", type: "number", value: String(s.qaCap), min: 0, max: 50, required: true, range: "0–50" },
    { name: "qa_owner", label: "QA intake owner", value: s.qaOwner, maxLength: 64, range: "user name", hint: "Only when auto-create without approval is on: whose connection creates them. Empty: the shared connection's owner." },
    { name: "fixer_trigger", label: "Fixer trigger", type: "select", value: s.fixerTrigger === "column" ? "column" : "off", range: "choice", options: [{ value: "off", label: "off" }, { value: "column", label: "column" }],
      hint: "Column: the old Ready-for-fixer column, status and label start the fixer. Off: only Spin up agent and Take action reach it." },
    { name: "chat", label: "Task chat", type: "check", on: s.chat, hint: "A thread per task; each message gets one read-only Argus reply, $0.15, within the task and daily budgets. Off: the old answers and instructions form." },
    { name: "secrets", label: "Task secrets", type: "check", on: s.secrets, hint: "An admin gives a task a credential the agent never sees; tmpfs only, wiped when the task ends, 24 h at most; never for release tasks. Off wipes every task secret." },
  ];
}

function Rows({ title, head, n, empty, note, children }: { title: string; head: string[]; n: number; empty: string; note?: string; children: React.ReactNode }) {
  return (
    <PlainCard as="section" aria-label={title}><div className="card-h"><h2>{title}</h2><span className="muted num">{n}</span></div>
      {note ? <p className="set-note">{note}</p> : null}
      {n ? <div className="tablewrap" role="region" aria-label={title} tabIndex={0}><table className="l-table"><thead><tr>{head.map((h) => <th key={h}>{h}</th>)}</tr></thead><tbody>{children}</tbody></table></div> : <p className="empty">{empty}</p>}
    </PlainCard>
  );
}

/** Settings → Jira sync: the connection, what failed, the task agent and the hosts Argus may call. */
export default async function JiraSyncPage({ searchParams }: { searchParams: Search }) {
  const q = await pick(searchParams, ["req", "jira"]);
  const [r, csrf, flash] = await Promise.all([apiGet<AdminBoard>("/api/v1/admin/board" + qs(q)), csrfToken(), readFlash()]);
  const title = <h1>Jira sync</h1>;
  if (r.status === 403) return <><header className="phead"><div>{title}<HelpQ p={PAGE} /></div></header><AdminsOnly /></>;
  if (r.status !== 200) throw new Error(`/api/v1/admin/board answered ${r.status}`);
  const d = r.data;
  const n = d.agent.fixerTasks.length;
  return (
    <>
      <Refresh on={!!d.refresh} />
      <header className="phead"><div>{title}<HelpQ p={PAGE} /><p className="psub">{d.project} · {d.queued} queued</p></div></header>
      <SettingsTabs cur={PAGE} />
      <div className="stack">
        <Banners list={[...d.notices, ...d.state, ...d.problems.map((text) => ({ text, kind: "fail" as const }))]} flash={flash} />
        <form action={settingsGo} className="row-act"><Hidden csrf={csrf} _to="board" /><Button name="action" value="sync" variant="outline">Sync now</Button><Button name="action" value="full" variant="outline">Full reconcile</Button></form>
      </div>
      <div className="detail">
        <div className="stack">
          {d.shared ? (
            <PlainCard as="section" id="shared" aria-labelledby="j-s"><div className="card-h"><h2 id="j-s">Shared Jira connection</h2></div>
              <dl className="rec">{d.shared.facts.map((f) => <div key={f.label}><dt>{f.label}</dt><dd className={f.mono ? "mono" : undefined}>{f.value}</dd></div>)}</dl>
              <SetForm to="jira-shared" page={`${PAGE}#shared`} csrf={csrf} form="jira-settings" hidden={{ action: "shared-settings" }} fields={sharedFields(d.shared)} submit="Save settings" secondary stack note="The board sync applies it; the result shows at the top of this page." />
              <SetForm foldTitle="Replace the token, URL or CA" to="jira-shared" page={`${PAGE}#shared`} csrf={csrf} form="jira-connect" hidden={{ action: "shared-connect" }} who={d.user} fields={connectFields(d)} submit="Test and save" stack note={`Argus tests the token from ${d.host}; the result shows at the top of this page.`} />
              <details className="fold"><summary>Disconnect…</summary><form action={settingsGo} className="u-confirm"><Hidden csrf={csrf} _to="jira-shared" action="shared-disconnect" />
                <Button variant="destructive" size="sm">Disconnect shared Jira</Button><span className="muted small">Deletes the shared token on {d.host}; personal connections stay.</span></form></details>
              <details className="fold"><summary>How it works</summary><Steps host={d.host} /></details>
            </PlainCard>
          ) : (
            <PlainCard as="section" id="shared" aria-labelledby="j-c"><div className="card-h"><h2 id="j-c">Connect Jira</h2></div><Steps host={d.host} />
              <SetForm to="jira-shared" page={`${PAGE}#shared`} csrf={csrf} form="jira-connect" hidden={{ action: "shared-connect" }} who={d.user} fields={connectFields(d)} submit="Test and save" stack note={`Argus tests the token from ${d.host}; the result shows at the top of this page.`} /></PlainCard>
          )}
          <PlainCard as="section" aria-labelledby="j-set"><div className="card-h"><h2 id="j-set">Settings</h2></div>
            <SetForm to="board" page={PAGE} csrf={csrf} form="board-settings" hidden={{ action: "settings" }} secondary submit="Save settings" note={applied}
              fields={[{ name: "auto_incidents", type: "check", on: d.autoIncidents, label: "Propose a task for every page-level incident (production, customer-facing, critical)",
                hint: <>At most 5 a day; an admin accepts it in <a href="/review?kind=proposals">Review</a>. Never runnable by the fixer.</> }]} /></PlainCard>
          <Rows title="Failed or dropped" head={["When", "Change", "Task", "Reason"]} n={d.failed.length} empty="Nothing failed." note="Jira wins a conflict: a move made here is dropped when Jira changed the task meanwhile.">
            {d.failed.map((x, i) => <tr key={i}><td data-label="When"><When ts={x.at} /></td><td data-label="Change"><span className="pill neutral">{x.kind}</span></td><td data-label="Task"><a href={`/board/task?id=${x.taskId}`}>{x.key}</a></td><td data-label="Reason">{x.error}</td></tr>)}</Rows>
          <Rows title="Retrying" head={["Next try", "Change", "Tries", "Last error"]} n={d.retrying.length} empty="Nothing waiting for a retry.">
            {d.retrying.map((x, i) => <tr key={i}><td data-label="Next try"><When ts={x.at} /></td><td data-label="Change"><span className="pill neutral">{x.kind}</span></td><td className="num" data-label="Tries">{x.attempts}</td><td data-label="Last error">{x.error}</td></tr>)}</Rows>
          <PlainCard as="section" id="agent" aria-labelledby="j-a"><div className="card-h"><h2 id="j-a">Task agent and QA intake</h2></div>
            <p className="set-note">Argus plans (read-only) every issue in the agent column assigned to these admins, asks its questions, and works it after an admin spins up the agent. Last tick: {d.agent.tick}{d.agent.error ? <> · <span className="set-err">{d.agent.error}</span></> : null}</p>
            <SetForm to="agent" page={`${PAGE}#agent`} csrf={csrf} form="agent" fields={agentFields(d)} secondary submit="Save agent settings" note={applied} /></PlainCard>
          <PlainCard as="section" id="fixer-migration" aria-labelledby="j-f"><div className="card-h"><h2 id="j-f">Retire the fixer column</h2></div>
            {n ? <>
              <p className="set-note">{n} task{n === 1 ? "" : "s"} in Ready for fixer.</p>
              <div className="tablewrap" role="region" aria-label="Ready for fixer" tabIndex={0}><table className="l-table"><thead><tr><th>Task</th><th>Tracker</th><th>Env</th><th>Fixer run</th><th>Title</th></tr></thead>
                <tbody>{d.agent.fixerTasks.map((x) => <tr key={x.id}><td data-label="Task"><a href={`/board/task?id=${x.id}`}>{x.key}</a></td><td data-label="Tracker">{x.tracker}</td><td data-label="Env">{x.env}</td><td data-label="Fixer run">{x.fixState}</td><td data-label="Title">{x.title}</td></tr>)}</tbody></table></div>
              <form action={settingsGo} className="row-act"><Hidden csrf={csrf} _to="fixer-move" expect={String(n)} /><Button variant="outline">Move these {n} to In Progress</Button>
                <span className="muted small">Jira: a transition with your token; GitLab: the list label. Running fix runs finish and are left alone.</span></form>
            </> : <p className="set-note">No task is in Ready for fixer.</p>}</PlainCard>
          <PlainCard as="section" id="hosts" aria-labelledby="j-h"><div className="card-h"><h2 id="j-h">Allowed Jira hosts</h2></div>
            <p className="set-note">Argus calls only these hosts with a user&apos;s token (https, port 443 unless written as host:port; never loopback, link-local or metadata addresses; no redirects; only <code>/rest/api/2</code> and <code>/rest/agile/1.0</code>). An admin connecting their own account to a new host adds it here; every change is in the audit log.</p>
            <SetForm to="jira-hosts" page={`${PAGE}#hosts`} csrf={csrf} form="jira-hosts" stack secondary submit="Save hosts" note="Every change is in the audit log."
              fields={[{ name: "hosts", label: "Allowed Jira hosts (one per line, host or host:port, at most 20)", type: "area", value: d.hosts, placeholder: "jira.example.com" }]} /></PlainCard>
          <Rows title="Connections" head={["User", "Jira identity", "Mode", "Projects", "Last sync", "Last error"]} n={d.connections.length} empty="Nobody has connected Jira yet. Each user does it in Account.">
            {d.connections.map((x) => <tr key={x.user}><td className="mono" data-label="User">{x.user}</td><td data-label="Jira identity">{x.identity}{x.host ? <div className="muted small mono">{x.host}</div> : null}</td><td data-label="Mode"><span className="pill neutral">{x.mode}</span></td><td className="mono small" data-label="Projects">{x.projects || "-"}</td><td data-label="Last sync"><When ts={x.lastSync} /></td><td data-label="Last error">{x.lastError || "-"}</td></tr>)}</Rows>
          <PlainCard as="section" aria-labelledby="j-w"><div className="card-h"><h2 id="j-w">How it syncs</h2></div>
            <p className="set-note">Every 2 minutes and at once after a change here: queued creates, moves (by workflow transition), assignments and comments go to Jira; issues changed in the last minutes come back (JQL <code>project = X AND updated &gt;= -5m</code>); a full reconcile runs nightly. Text sent to Jira passes the secret scan. A task in In Progress gets a read-only plan; an admin&apos;s Spin up agent hands the approved WRITE steps on develop or pre-release to the fixer (the old Ready-for-fixer trigger only with the fixer trigger set to column). The result is posted to the tracker. Production tasks are never run: Spin up writes a proposal for the operator.</p></PlainCard>
        </div>
        <aside><Rail title="Connection" facts={d.connection} /></aside>
      </div>
    </>
  );
}

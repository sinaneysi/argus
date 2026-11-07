---
title: What's new
summary: A dated list of the major features, newest first.
order: 11
---

## 2026-11-07 {#d2026-11-07}

- **A 2FA reset forces a new setup.** After an admin resets someone's 2FA, their next sign-in opens only the 2FA
  setup until they confirm a new app ([Sign-in and two-factor](/help/safety#sign-in)). Users and access has one
  Actions menu per row with a separate confirm step for each risky action ([Users and access](/help/settings#users)).

- **Marketing → SEO.** Googlebot health per edge, Search Console figures, the index state of the key URLs and the
  day's suggestions, with Send to chat ([SEO](/help/marketing#seo)).

## 2026-11-04 {#d2026-11-04}

- **Command palette and shortcuts.** Press Ctrl K on any page to find a page, an action or an open incident, and press ?
  to see every shortcut ([Keyboard shortcuts](/help/overview#shortcuts)).

## 2026-11-03 {#d2026-11-03}

- **Settings → Data sources.** One list of every system Argus reads (GitLab, Grafana, Kubernetes, Outline,
  Elasticsearch/OpenSearch, Sentry), as many of each as you have. Each login is checked: does it work, and is the
  agent's read-only. The existing GitLab, Grafana, Outline and Kubernetes connections appear there on their own
  ([Data sources](/help/settings#sources)).
- **Settings → Kubernetes.** Paste or upload a kubeconfig per cluster and the agent can read pods, logs and events
  there (`argus tools kube`). Argus checks what the login may do and enables only a read-only one; a kubeconfig that
  can write or read Secrets is refused (an admin can override on develop and pre-release, never on release). The page
  shows how to make a read-only ServiceAccount ([Kubernetes](/help/settings#kubernetes)).

## 2026-11-02 {#d2026-11-02}

- **Settings → Claude, simpler.** The page now opens on one answer, "Is Argus's AI working right now?", with the
  reason and the one thing to do when it is not. Token, location guard and usage follow in that order, with the rarely
  used settings folded ([Claude](/help/settings#claude)).
- **Settings → Claude.** Replace the Claude token from the browser: paste the token from `claude setup-token`,
  Argus tests it and puts the previous one back if the test fails. **Test current token** shows whether the login
  works. A new egress country guard stops every Claude run while the traffic would leave from a blocked country
  (default IR) and says why ([Claude](/help/settings#claude)).
- **Review inbox: queue and review panel.** The queue is ordered by urgency (severity, production first, the nearest
  decision deadline, then waiting time) and says so; oldest, newest and expiring first are one choice away. One
  toolbar (search, severity, environment, source, sort) works in All and in every kind. A decision opens in a panel
  beside the queue, which keeps its place; the address can be shared. The panel shows the evidence (or says plainly
  that none was recorded), what accepting does, and one primary action; action approvals go through a plan review
  step with the pinned version. Snooze is no longer offered on a proposal that would expire first ([Review](/help/review)).

## 2026-10-31 {#d2026-10-31}

- **The sidebar opens and closes.** Six groups (Home, Incidents, Infrastructure, Delivery, Work and Agent) list
  their pages right in the sidebar; the group you are in is open, click any other group's name to open it. A closed
  group shows the sum of its counts. The tab rows above the page titles are gone; Check a repo has its own link
  ([Find your way](/help/overview#navigation)).
- **Review shows one list.** All is now every waiting decision in one list, the longest waiting first, each row
  naming its kind ([Review](/help/review)).

## 2026-10-30 {#d2026-10-30}

- **Argus 2 navigation: seven sections and one Review inbox.** The sidebar is now Today, Review, Operations,
  Incidents, Work, Delivery & Estate and Argus; Settings and Help sit at the bottom. A section's pages are tabs, and
  its first tab starts with **Needs you**: how much needs a person there and about how long it takes
  ([Find your way](/help/overview#navigation)).
- **Review: every decision in one place.** Task proposals, plan approvals, lessons, ideas, chat requests and problem
  links wait in one inbox with one count ([Review](/help/review)). Old addresses lead to their new place.
- **Today is the landing page.** The Overview is merged into Today; the scheduled checks moved to Infrastructure → Checks.
- **The search-engine page and the UI self-review are gone** from Argus; they were not operations work.

## 2026-10-27 {#d2026-10-27}

- **Argus learns, with your OK.** Task runs propose short lessons; an admin approves them on Argus agent →
  **Lessons**, and only approved ones help later runs. Everyone can read them on **What Argus knows**
  ([Lessons](/help/agent#lessons)).
- **Argus learns, with your OK.** Task runs propose short lessons; an admin approves them on Argus agent →
  **Lessons**, and only approved ones help later runs. Everyone can read them on **What Argus knows**
  ([Lessons](/help/agent#lessons)).
- **Ask Argus for a new task.** In a task's chat or the new "Ask Argus" thread on the board, ask Argus to define a
  task: it lands in [Proposed tasks](/review?kind=proposals) for an admin to accept into Jira or GitLab, never created directly.
- **Temporary credentials for a task.** When a develop or pre-release task needs an API token, an admin provides it on
  the task page (Provide secret). Argus's agent never sees it: a read-only command runs with it, reaches only the
  hosts you named, and the value is removed from its output; it is wiped when the task ends or expires (4 h by
  default, 24 h at most). Never for production tasks. Off until an admin turns it on
  ([Is it safe?](/help/safety#enforced)).
- **Temporary credentials for a task.** When a develop or pre-release task needs an API token, an admin provides it on
  the task page (Provide secret). Argus's agent never sees it: a read-only command runs with it, reaches only the
  hosts you named, and the value is removed from its output; it is wiped when the task ends or expires (4 h by
  default, 24 h at most). Never for production tasks. Off until an admin turns it on
  ([Is it safe?](/help/safety#enforced)).
- **Chat with Argus on a task.** A thread on every task Argus works: ask, answer its questions or correct the plan;
  each message gets one short read-only reply, and a change of plan makes Argus plan again. Viewers read, operators
  and admins post; admins turn it on in Settings → Jira sync → Task agent ([Task agent](/help/delivery#task-agent)).
- **Task runs see no credentials and leave no transcript.** Argus's task intake and run start Claude with only its
  login (no GitLab, Matrix, Jira or Grafana token) and a throwaway config dir; no Claude run keeps a session
  transcript on disk any more ([Agent runs](/help/agent#agent-runs)).
- **In Progress is the agent column.** A task that stays in In Progress for 2 minutes gets a plan on its card; an admin
  presses **Spin up agent** to run exactly that plan (production: **Write proposal for the operator**). The Ready for
  fixer column is retired; admins move the remaining tasks in one click ([Task agent](/help/delivery#task-agent)).
- **2FA once per sign-in.** The code you type at sign-in covers every change in that session; only turning off your
  2FA and new recovery codes ask again ([Sign-in and two-factor](/help/safety#sign-in)).
- **Claude usage switch.** Settings → General → **Claude usage** On/Off stops every Claude call Argus makes (your
  subscription); skipped runs are not failures, and Overview and Today say **Claude paused**
  ([General](/help/settings#general)).
- **GitLab tasks get everything Jira tasks have.** Argus's task agent works GitLab issues in In Progress, Matrix
  commands take `#45` / `infra#45`, forwarded requests can go to GitLab (`in gitlab`), QA intake proposes with GitLab
  alone, and moving an issue to the fixer list in GitLab starts the fixer for admins
  ([GitLab issues](/help/delivery#gitlab-issues)).
- **Faster answers to QA reports.** A new {{qa_room}} message from outside the DevOps team starts triage about 2 minutes
  later instead of at the next half hour; follow-up questions arrive while the reporter is still there.
- **Faster pages.** The app now has 2 CPUs and 512 MB instead of half a CPU, so one heavy page no longer slows the rest.
- **GitLab group boards.** Paste a group board URL and a group access token: Argus mirrors the issues of every project
  in the group, cards say **GitLab infra#45**, and new issues go to the default project or the one you pick
  ([GitLab issues](/help/delivery#gitlab-issues)).
- **GitLab issues beside Jira.** Connect one GitLab issue board in Settings → Integrations; both trackers work at once.
  Every card says **Jira** or **GitLab**, the board has tabs and a tracker filter, and New task and Accept ask where the
  task goes ([GitLab issues](/help/delivery#gitlab-issues)).
- **QA follow-up questions** never go to the DevOps team, stop once a person has answered, ask about a screenshot
  only when there is one, and use the reporter's language for a report that is only a link
  ([Follow-up questions](/help/operations#qa-questions)).
- **Read images in QA messages** (Settings, off by default): Argus reads the error and page from screenshots.

## 2026-10-25 {#d2026-10-25}

- **Keep me signed in** for 30 days on the sign-in page, and **Your sessions** in Account with sign-out per session.
- **Two-factor authentication** with Google Authenticator or any authenticator app; every change in Argus now needs
  it, reading does not ([Sign-in and two-factor](/help/safety#sign-in)).
- **Mark as OK** for a failing backup set you checked, with a reason and an end
  ([Mark as OK](/help/infrastructure#backups-accept)).
- **Clearer pages**: Act-now lines open the exact item (the backup set, the VM, the QA report), viewers see who acts
  instead of being told to act, CI/CD and Backups put what needs attention first, the incident page shows severity and
  owner next to the title, and "Lazy reading" is now called **Summary**.

## 2026-10-24 {#d2026-10-24}

- **Credentials** in Settings: when each token or key Argus uses expires, with warnings at 30, 14 and 7 days and
  how to rotate it ([Credentials](/help/settings#credentials)).

- **Help** in the sidebar: these pages, a search box, a **?** next to each page title, and a
  [printable version](/help/all).

## 2026-10-22 {#d2026-10-22}

- **Problems**: one record per root cause, links between issues, Argus's link suggestions, and states from new to
  closed ([Problems](/help/operations#problems)).
- **Vulnerabilities**: daily scan of hosts, containers and dependencies with P1 to P4, who is affected (host and
  service), blind spots, suppressions and fix prompts ([Vulnerabilities](/help/security#vulnerabilities)).
- **Check a repo**: is a repo ready for the shared CI/CD pipeline? ([Check a repo](/help/delivery#check-a-repo)).
- **Ideas**: three advisers propose improvements weekly; accept for a build prompt ([Ideas](/help/agent#ideas)).
- **Proposed tasks**: the approval queue; nothing goes to Jira without an admin
  ([Proposed tasks](/help/delivery#proposed-tasks)).
- **Lazy reading**: every report starts with five short lines.
- **QA follow-up questions** are fewer and to the point, in the reporter's language.
- The web app now runs as a single, faster Go program; pages and addresses did not change.

## 2026-10-21 {#d2026-10-21}

- **How to fix it** and **Prompt for a write-access agent** on every RCA, incident, problem, QA, VM and task page.
- **QA reports**: every ops-qa message and what Argus did with it; QA issues and follow-up questions.
- **QA reports**: every {{qa_room}} message and what Argus did with it; QA issues and follow-up questions.
- **CI/CD guardian**: failures and classes, stuck pipelines, branch and variable audit, green by environment, and
  post-deploy checks ([CI/CD](/help/delivery#ci-cd)).
- **Capacity** covers every VM and every filesystem, with growth per day and days to full.
- **Find a VM** in the sidebar, on Inventory and on Capacity.
- **Outline service docs** in preview ([Outline](/help/agent#outline)).
- **Theme switch**: Auto, Light, Dark.
- Grouped sidebar, Settings with tabs, and long lists capped with "Show all".
- Hub services raise incident severity; the sales-event readiness card and brief section.

## 2026-10-20 {#d2026-10-20}

- **Task board** mirrors the Jira board; connect your own Jira in Account ([Task board](/help/delivery#task-board)).
- **Task agent**: Argus plans Jira tasks, asks questions, and starts after an admin approves the plan.
- **Matrix requests**: forward a message to the bot, check the preview, say yes
  ([Matrix requests](/help/delivery#matrix-requests)).
- **New backup in four fields**, Backup targets, Coverage and Install now ([Backups](/help/infrastructure#backups)).
- **Deep RCA** and automatic deep RCA ([Deep RCA](/help/operations#deep-rca)).
- **Inventory** with the VM page, pressure, services and dead upstreams; VM and storage analyses.
- **Sign in with Keycloak**, with an SSO-only option.

## 2026-10-19 {#d2026-10-19}

- **Incidents**: one record per problem, acknowledge and resolve, auto-resolve and re-open.
- **Take action**: the fixer on develop and pre-release, after an admin's review.
- Daily brief in the digest, capacity forecast and **Alert hygiene**.
- **Certificates** for about 300 public names, grouped by certificate.
- **Argus health** on the Overview, and a nightly encrypted backup of Argus's own data.

## 2026-10-18 {#d2026-10-18}

- The web app with its own sign-in, users and roles.
- **Alerts** from both data centres, live.
- Real-time alert watcher: investigates alerts that stay firing.
- Chat triage of {{qa_room}} and Kuma, and readable Matrix notices.
- **Scan now** on each page.

## 2026-10-17 {#d2026-10-17}

- Argus starts: daily backup check of 127 backup sets, certificate check and log RCA.

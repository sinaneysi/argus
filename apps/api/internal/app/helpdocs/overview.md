---
title: Getting started
summary: What Argus is, the read-only principle, environments, sign-in, roles and finding your way.
order: 1
---

Read this page first. It explains what Argus is for, what it will never do, and the parts every page shares.

## What Argus is {#what-is-argus}

Argus is the {{company}} DevOps assistant. It runs on one server and does three things:

- **Watches.** Alerts from both data centres, Uptime Kuma, the {{qa_room}} room, CI/CD pipelines, backups,
  certificates, disk and memory, and known vulnerabilities.
- **Investigates.** When something is wrong it collects evidence through read-only accounts and writes a root-cause
  analysis (RCA): what broke, why, the impact and the options to fix it.
- **Reports.** Everything it finds lands in this web app. Urgent items also go to the team's Matrix room; the rest
  goes into a digest twice a day (see [Notifications](/help/notifications)).

Most of the work is scheduled. Some of it is started by people from this app: "Scan now", "Investigate deeply",
"Analyse", "Check this repo", "Take action".

## The read-only principle {#read-only}

- Argus **reads**. Metrics, logs, chat, GitLab and hosts are read through read-only accounts, with a fixed list of
  allowed commands.
- It **writes only its own records**: its database, the pages you see, and proposals.
- It **never changes production**. A fix for production is always a proposal. A person runs it, after the operator
  approved that one change.
- On **develop and pre-release** an admin can let Argus carry out a fix that the admin reviewed first
  ([Take action](/help/operations#take-action)). The operator decides which of the two environments are allowed.
- **Nothing goes to Jira without a person.** Automatic paths only propose tasks
  ([Proposed tasks](/help/delivery#proposed-tasks)).

The full list per environment is on [Is it safe?](/help/safety).

## Environments {#environments}

| Environment | Also called | What Argus may do |
|---|---|---|
| develop | stage, dev | Read. Fix through the fixer after an admin's review, when the operator allowed it. |
| pre-release | pre, pre-prod | Read. Fix through the fixer after an admin's review, when the operator allowed it. |
| release | production, prod | Read only. Every fix is a proposal that a person runs. |

Pages show the environment as a tag (`develop`, `pre-release`, `release`). Matrix messages use short badges:
STAGE, PRE and PROD, plus {{site_tags}} for the data centre. When a page cannot tell the
environment, treat it as production.

## What needs a human {#approval}

| Action | Who decides |
|---|---|
| Any change on production | The operator approves each change; a person runs it. |
| A fix on develop or pre-release | An admin reviews and edits the prompt, ticks "I reviewed", then runs it. |
| A new Jira issue from Argus | An admin accepts it in Review, or the forwarder says yes in Matrix. |
| The task agent starting work | An admin presses Start on one exact plan version. |
| A link between an item and a problem | An admin. A viewer's link is a suggestion. |
| Hiding a vulnerability | An admin, with a reason and an expiry date. |
| Accepting or rejecting an idea | An admin. |
| Settings, users, Jira, backup targets | Admins; root on the server applies and logs every change. |

## Sign in {#sign-in}

There are two ways in:

- **Sign in with Keycloak** (single sign-on), when it is configured. Your Keycloak account needs the role `viewer`
  or `admin` on the Argus client. Without a role, Argus refuses the sign-in and says why.
- **A local account**: user name and password, created by an admin.

Good to know:

- A new or reset password is temporary. Argus asks you to choose your own before you can continue.
- Five wrong passwords lock that user name from your address for 15 minutes.
- A session lasts 12 hours, and ends after 2 hours without activity.
- Keycloak users change their password in Keycloak, not in Argus.
- Admins can switch on "SSO only". Then password sign-in is refused, except for one break-glass account.
- Log out with the arrow button next to your name. With Keycloak you are also signed out of Keycloak.

Your name at the bottom of the sidebar opens **Account**: change your password, connect your own Jira, and set your
Matrix ID.

## Roles {#roles}

| | viewer | admin |
|---|---|---|
| Read every work page | yes | yes |
| Scan now, Refresh a VM, Check a repo | yes | yes |
| Acknowledge an incident | yes | yes |
| Create, move, assign or comment on a task | yes | yes |
| Suggest a problem link | yes | yes (and accept it) |
| Resolve or re-open an incident | no | yes |
| Investigate deeply, Analyse a VM or a disk | no | yes |
| Take action (fixer), Fix runs | no | yes |
| Review: accept, approve or reject | no | yes |
| Test and save a backup, Install now | no | yes |
| Suppress a vulnerability, decide ideas | no | yes |
| Settings | no | yes |

Moving or commenting on a Jira task also needs your own read-and-write Jira connection
(see [Task board](/help/delivery#task-board)).

## Find your way {#navigation}

The sidebar has six groups. Each lists its pages; click a group's name to open or close it. The group of the page
you are on is open, the others are closed. A closed group shows the sum of its counts (admins), so nothing is hidden.

- **Home**: Today (the day's plan, worst first) and the Review inbox (every decision in one place: task proposals,
  action approvals, lessons, ideas, chat requests and problem links, see [Review](/help/review)).
- **Incidents**: what is broken, and why? Incidents, Alerts, Problems (when switched on), RCA library.
- **Infrastructure**: what do we run, and is it healthy? Inventory, Capacity, Certificates, Backups, Checks, Alert
  hygiene.
- **Delivery**: what do we ship, and is it safe? CI/CD, Check a repo, Vulnerabilities.
- **Work**: what is being done, and by whom? Tasks, Conversations, New task.
- **Agent**: what is the agent doing? Runs, Knowledge, Fix runs (admins).

The first page of a group starts with **Needs you**: how many things need a person there and about how long they
take. Old addresses (for example `/proposals` or `/ideas`) still work and lead to their new place.

At the bottom: **Help** (these pages), **Settings** (admins), the theme switch and your name. On a phone, open
**Menu** at the top right.

Every page follows the same pattern. The line under the title answers the page's question first. Long lists show
15 rows with "Show all". Healthy rows are folded away; problems stay open. The **?** next to a page title opens
the help for that page.

### Scan now {#scan-now}

Pages with a scheduled check have a **Scan now** button at the top right, with the time of the last scan. It asks
Argus to run that check again now. Anyone signed in can press it. The server runs each check at most once every
2 minutes and 40 times a day; the button shows "Scanning…" while it runs and the page refreshes itself.

## Find a VM {#find-a-vm}

The **Find a VM** box under the logo searches every server Argus knows.

- Type a name, an IP, an environment, a role, a service, a domain or a mount point.
- Several words must all match. Upper and lower case do not matter.
- One match (or an exact name or IP) opens that VM's page. Otherwise the [Inventory](/inventory) lists the
  matches, with the matching words highlighted.

The same search is on [Inventory](/help/infrastructure#inventory) and on Capacity → All VMs.

## Keyboard shortcuts {#shortcuts}

Press **Ctrl K** (⌘K on a Mac) on any page to open the command palette. Type to find a page, an action or an open
incident, then press Enter. Press **?** to see every shortcut.

Jump to a page with **g**, then a letter: `g t` Today, `g r` Review inbox, `g i` Incidents, `g a` Alerts, `g p` Problems,
`g c` Checks, `g n` Inventory, `g s` Certificates, `g k` Backups, `g d` CI/CD, `g v` Vulnerabilities, `g b` Tasks,
`g q` Conversations, `g u` Runs, `g w` Knowledge.

Other keys: `c` New task, `a` Ask Argus, `/` Find a VM, `Shift R` Re-plan today, `Shift T` light or dark, and `j` `k` to
move through the rows of a list, with Enter to open the marked row. Shortcuts do nothing while you type in a field. To
turn the single-key shortcuts off, choose **Turn single-key shortcuts off** in the palette.

## Theme {#theme}

The three buttons above your name switch the look: **Auto** (follows your system), **Light** and **Dark**. The
choice is kept in this browser for a year. It changes only the colours.

## Reading a report {#reading-a-report}

Incidents, RCA reports, problems, VM and disk analyses and vulnerability groups share the same pieces:

- **Summary** comes first: five short lines. What broke, Why, Impact, Fix and Fix status. Read this when you
  have ten seconds.
- **Full analysis** is folded under it. Each claim is marked **proven**, **probable** or **unproven**.
- **How to fix it** lists one to four options. Each has a kind (mitigation, permanent fix, prevention), why it works,
  effort, risk and blast radius. One is marked **Recommended**. The approval tag says "Operator OK first" or
  "Fixer can do this"; on production the operator approves every option.
- **Prompt for a write-access agent** is a complete, self-contained instruction. Click it to select all, or use
  **Download .md**. On production it is a proposal that a person runs; on develop and pre-release an admin can
  hand it to the fixer.

A section Argus could not produce says "Not generated" with the reason. Argus removes secrets from reports
before they are stored.

## FAQ {#faq}

:::details I can sign in but some pages or buttons are missing.
Most of Review, Fix runs and Settings are for admins. The Problems page appears only when the feature is
switched on. Ask an admin if you need the admin role.
:::

:::details Keycloak says I have no Argus role.
Ask a Keycloak admin to give your account the client role `viewer` (or `admin`) on the Argus client. Sign in again
afterwards.
:::

:::details Is it safe to press Scan now?
Yes. It only re-runs a read-only check. The server limits how often it runs.
:::

:::details Where do the times come from?
Times are shown in {{tz}} time.
:::

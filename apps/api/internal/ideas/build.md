# Build {{product}} feature: {{title}}

You are a coding agent working in {{repo}}.
Build the feature below end to end: code, configuration, tests, documentation and verification.
This prompt is self-contained; read the files it names before you change anything.

## Goal
{{goal}}

## User story
As {{operator}}, I want {{want}}, so that {{so_that}}

## Why: the evidence
Idea #{{id}}, proposed by {{persona_name}} ({{persona_role}}) on {{created}}, accepted by {{accepted_by}} on {{accepted}}.
{{changed}}
Problem:
{{problem}}

Evidence (pages of {{product}}: {{public_url}}):
{{evidence}}

## What to build
{{proposal}}

- **Value:** {{value}}
- **Effort (estimate):** {{effort}}
- **Risk:** {{risk}}
- **Prerequisites:** {{prerequisites}}
- **Success metric:** {{success_metric}}

## Scope and non-goals
- In scope: the proposal above, its configuration, tests and documentation.
- Not in scope: anything the proposal does not name; changes to unrelated pages or jobs; new access for {{product}}
  (if the feature needs a token, an account or a firewall rule, stop and list it under "Needs from the operator").
- If a prerequisite is missing, build what does not depend on it behind a switch (off) and report the gap.

## Where in the code (area: {{area}})
Start here, then follow the imports:
{{area_paths}}

Read first: `README.md`, `AGENTS.md`, `DESIGN.md` (UI changes) and the ADRs in `docs/`.

## Rules
1. Every installation-specific value is configuration with a default, never a literal in code or prompts.
2. Production systems are read-only for {{product}}: a change there is a proposal with a backup, a rollback and
   a person's approval.
3. Tests use synthetic data only; never copy live data.
4. Small commits on a branch; never force-push. {{commit_trailer}}

## Tests (all must pass)
Run the repository's gates (Go vet and tests, lint, type check, web tests, build) and add tests for the new behaviour.

## Report
What was built, the tests that cover it, what is still open, and "Needs from the operator".

# ADR-0006: Environments as one data file

Status: proposed, 2026-10-22.

## Context
Which address, hostname or domain belongs to which environment is encoded five times in different forms:
guard regexes, `fix-requests.py` networks and a copied regex list, `ops-fix@.service` IP filters,
`ssh-ro.sh` alias grammar, the fixer setup script. These are safety controls; drift between them is a
production risk, and they are the main obstacle to adoption.

## Decision
- `environments.json` (CONFIG.md §8) is the single source: names, aliases, **class**
  (dev/staging/production), CIDRs, carve-outs, hostname patterns, domains, monitoring labels, alias
  grammar, CI branch. Unknown = production.
- Code branches on class, never on names.
- Root `argus-config render` produces each consumer's form (guard markers JSON, unit drop-in with
  `IPAddressAllow/Deny`, dispatcher network table, ssh-ro alias table). Safety consumers fail closed when
  the rendered file is missing or older than the source.
- A golden test asserts that Northwind's `environments.json` renders exactly today's literals before
  the literals are removed.

## Consequences
- One review for a network change; the guard, fixer and ssh-ro can no longer disagree.
- The guard used on the operator workstation (context repo) can read the same file.

# ADR-0007: Per-action SSH certificates and target-side capability scripts

Status: proposed, 2026-10-22.

## Context
Host writes need a credential. Today: `argus-ro` keys (read; enforcement on argus-01 only) and the
`argus-fix` key with `NOPASSWD: ALL` (develop/pre-release). A standing production write key on argus-01
would be the most valuable secret on a host that also runs a model reading untrusted text.

## Decision
- An SSH CA on argus-01 (root 0400, preferably in a root-only ssh-agent or token). Per action and target the
  broker signs an ephemeral key: principal `argus-act`, validity `ACTION_CERT_TTL` (5 min), key id = action
  id, `force-command=/usr/local/sbin/argus-cap`, `source-address=ARGUS_HOST_IP`, no forwarding, no pty.
- Targets trust the CA only for `argus-act`; `argus-cap` runs only capability scripts enabled on that host
  (`/etc/argus-cap/enabled`), each with plan/apply/verify/rollback, through a sudoers rule limited to
  `/usr/local/lib/argus-cap/*`.
- The operator installs and enables per host with a dry-run-first setup script; Argus never does.
- Later, the same forced-command pattern replaces the client-side-only enforcement of `argus-ro`.

## Alternatives rejected
- **HashiCorp Vault SSH secrets engine:** same model, extra service to run and secure; can replace the
  local CA later without changing targets (they trust a CA either way).
- **Static per-capability keys with `command=` in authorized_keys:** standing credentials; rotation and
  revocation are manual.
- **Agent on each host pulling jobs:** inverse trust direction, more software on production hosts.

## Consequences
- No standing write credential for production anywhere except the CA key, which never leaves root.
- sshd logs tie every production change to an action id.

# ADR-0019: Access brokers: Teleport and Boundary for reaching VMs and Kubernetes

Status: accepted, 2026-11-06. Phase 1 is built. Phases 2 and 3 wait for broker identities.

## Decisions taken with the operator

- People and the agent both use the broker.
- Teleport first, Boundary second, behind one adapter.
- The agent stays read-only everywhere, with the same fixed command lists as today. Writes keep the approval flow of
  ACCESS.md.

## Context

The agent reads clusters through root-owned kubeconfigs and hosts through a read-only SSH account. Those are long-lived
credentials on the Argus server. People connect on their own, so Argus cannot lead them from a problem to the machine.

## Decision

**D1. Argus never grants access.** The broker decides who reaches what, and it records the session. Argus shows the way
in. This keeps ACCESS.md's rule that Argus does not grant itself access.

**D2. Phase 1, people.** `internal/access` turns the install's broker settings into the commands for a VM or a cluster.
The VM page and Settings -> Kubernetes show them. Nothing connects from Argus, so no credential is added. Names come
from collected data, so a name that is not safe to paste shows no command.

**D3. Phase 2, the agent on Teleport.** A machine identity from `tbot`, renewed on the Argus server, replaces the stored
kubeconfig and SSH key. Its Teleport role is read-only and is the real boundary; the fixed command lists stay as a second
one. `kube.Tool` refuses kubeconfigs that run a program, so a broker kubeconfig needs its own checked path, not a
loosened rule. The read-only verdict still runs before a cluster is enabled for the agent.

**D4. Phase 3, Boundary** adds the same two paths behind the same adapter.

## What Phases 2 and 3 need from the operator

- A Teleport bot with a read-only role and a join token.
- A Boundary controller to test against.

## Consequences

Settings: `ARGUS_ACCESS_BROKER`, `ARGUS_TELEPORT_PROXY`, `ARGUS_ACCESS_SSH_USER`, `ARGUS_BOUNDARY_ADDR`,
`ARGUS_BOUNDARY_SCOPE`. With no broker set, nothing changes.

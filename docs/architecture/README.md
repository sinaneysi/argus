# Architecture documents

These documents were written for the reference installation Argus grew out of, before this repository existed.
They state the design (configuration, access policy, adapters, CVE, problem records) and still apply.
Where they name `bin/`, `scripts/`, `app/`, `apps/api/` or `prompts/`, they mean the Python collectors and the earlier
layout, which are not in this repository yet: see [../ROADMAP.md](../ROADMAP.md) for what was dropped and where each
part goes in Go. Links to `ROADMAP.md`, `HARDCODE-AUDIT.md` and ADRs 0005 and 0011-0015 point to documents that
stayed with the reference installation.

The current direction is [ADR-0016](adr/0016-argus-2-platform.md) and the [Argus 2 design set](argus-2/README.md).

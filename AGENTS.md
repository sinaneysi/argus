# Working on Argus (for people and coding agents)

Read this before changing anything a user sees.

- The design source of truth is `packages/design/DESIGN.md`. Before a new page or any UI change, read §4 (page
  templates), §9 (hard constraints) and **§10 (known mistakes: never repeat)**, and follow the new page checklist in §10.
- Reuse what exists: copy the closest existing page and its components (`apps/web/components/Section.tsx`,
  `components/ui/*`). A new page must look like the pages next to it, not like a new design.
- Check the result in a browser before merging: `node demo/layout-sweep.mjs <base-url>` (0 findings) and
  `python3 demo/smoke.py` against the demo.
- Gates: Go vet and tests (with and without `-tags demo`), web lint, typecheck, tests and build, `npm audit`, and the leak
  check (no company names, hosts or people in this repository).
- Security never changes as a side effect of a restyle (DESIGN.md §9).

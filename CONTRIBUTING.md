# Contributing

Thank you for helping. Pull requests against `main` are welcome.

1. Open an issue first for anything larger than a bug fix, so the direction can be agreed
   (the product direction is [ADR-0016](docs/architecture/adr/0016-argus-2-platform.md): Go backend, strict CSP; the UI is
   Next.js per [ADR-0018](docs/architecture/adr/0018-monorepo-next-go-api.md)).
2. Keep a pull request to one change, with tests.
3. Before you open it, in `apps/api`: `go vet ./...` and `go test ./...` (also with `-tags demo`), and
   in the repository root `npm run lint`, `npm run typecheck`, `npm test` and `npm run build` if you touched
   `apps/web`, and `docker compose -f demo/compose.yml up --build` if you touched a page.
4. Sign off your commits (`git commit -s`, Developer Certificate of Origin).
5. Use only synthetic data in tests and examples: fictional hosts (`node-a`, `prod-shop-01`), RFC 1918 or
   documentation address ranges, `example.com` / `northwind.example` domains, people such as `alex.doe`.
   Never real hostnames, addresses, names or credentials.

UI changes follow `packages/design/DESIGN_SYSTEM.md`.

Security problems: see [SECURITY.md](SECURITY.md), not a pull request.

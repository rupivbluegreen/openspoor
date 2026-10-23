# ADR-001: Go modular monolith shipped as one static binary

- Status: accepted
- Date: 2026-10-22

## Context
Open Spoor must run as managed SaaS, self-hosted multi-tenant, single tenant and air-gapped national hub. Operators at pharma plants and regulators need something they can install, validate (GAMP 5 / CSV) and upgrade without a platform team.

## Decision
- One Go binary (`openspoor`) with subcommands `api`, `worker`, `all`, `migrate`, `bootstrap`, `seed`. Mode is configuration (`OPENSPOOR_MODE=saas|single|hub`), never a fork.
- Bounded contexts live in `internal/<module>`; each exposes a `Module` (routes), optional `Worker` (jobs) and `Scheduled` (periodic tasks). They share only `internal/core` (DB, auth, audit, jobs, bus) and the reusable `pkg/` libraries (`pkg/epcis`, `pkg/gs1`).
- Boundaries are enforced in CI with go-arch-lint (`backend/.go-arch-lint.yml`) and depguard (`pkg/` may not import `internal/`).
- HTTP: chi; logging: slog JSON; metrics: Prometheus.

## Deviation from the original prompt
The initial plan called for sqlc and an oapi-codegen generated server. We hand-write pgx queries behind small generic helpers (`db.List`, `db.One`) and keep a hand-maintained OpenAPI 3.1 document (`internal/app/openapi.yaml`, served at `/api/v1/openapi.yaml`) that a test checks against the router. Reason: velocity while the schema is still moving; generated code can be adopted per module later without API changes. CI runs `oasdiff` on the spec for breaking changes.

## Consequences
Simple deploys and validation packs; one process can be scaled horizontally (api) and vertically (worker). Splitting a module out later means replacing in-process calls with its existing HTTP API.

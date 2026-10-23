# app

Wires every module into the single Open Spoor binary. `New` opens the database, event bus,
key ring and authenticator, builds the modules in dependency order (`modules.go`), registers
their jobs and mounts their routes. The package also runs scheduled tasks
(`RunSchedulers`, one PostgreSQL advisory lock per task so only one worker replica runs it),
serves the built web frontends, generates the OpenAPI document and loads demo seed data.

## Endpoints

| Method | Path | Purpose |
|---|---|---|
| GET | `/healthz` | Liveness |
| GET | `/readyz` | Readiness (database ping) |
| GET | `/metrics` | Prometheus metrics |
| GET | `/api/v1/meta` | Name, version, mode, module list, auth options (public) |
| GET | `/api/v1/openapi.yaml` | The committed OpenAPI document (public) |
| GET | any other non-API path | Frontends from `OPENSPOOR_WEB_ROOT` (`/verify/`, `/operator/`, `/` console) |

Each module's own routes are mounted under `/api/v1`: `Public` routes without auth,
`Routes` behind the auth middleware, and `Root` routes at the top level.

## Background work

None of its own. `RunSchedulers` drives every module's `Schedules()`; module jobs run on
the shared `core/jobs` runner.

## Data

No tables of its own. `Seed` drives the real API in-process to load demo data.

## Depends on

Every module under `internal/`, `core/*`, `pkg/epcis`, `pkg/gs1`.

## Spec gaps

None marked `TODO(spec)`.

## Tests

From `backend/`: `eval "$(../scripts/testdb.sh)" && go test ./internal/app`.
`web_test.go` runs without a database. If `openapi.yaml` is stale, regenerate it with
`UPDATE_OPENAPI=1 go test ./internal/app -run TestOpenAPIUpToDate` (needs the test database).

# core

Shared infrastructure every module receives. It has no HTTP routes of its own and does not
import any domain module.

| Package | Purpose |
|---|---|
| `kernel` | `Deps` (config, DB, bus, jobs, auth, key ring) and the module interfaces: `Module` (`Routes`), `PublicRoutes`, `RootRoutes`, `Worker` (`RegisterJobs`), `Scheduled` (`Schedules`) |
| `config` | Loads configuration from `OPENSPOOR_*` environment variables (plus `ANTHROPIC_API_KEY`); secret settings go through `secretsrc`; builds the server TLS configuration |
| `secretsrc` | Resolves a secret setting given as plain text, `file:/path` or `vault:<mount>/<path>#<field>` (KV v2 over `net/http`, token or Kubernetes login); fails closed, never logs values |
| `db` | PostgreSQL pool; tenant transactions set `app.tenant_id` so row-level security isolates tenants; `OperatorTx` for cross-tenant system work; `List`/`One`/`Count` helpers |
| `auth` | Authenticates requests (local JWT, OIDC, scoped API keys), carries the `Principal`, role checks (`Require`, `RequireOperator`) |
| `httpx` | JSON responses, typed HTTP errors, request decoding, handler adapter |
| `audit` | Append-only, hash-chained audit entries and chain verification |
| `jobs` | Durable PostgreSQL job queue with retries, exponential backoff, dedupe and run-at |
| `bus` | In-process domain events, mirrored to a Kafka-compatible log when `OPENSPOOR_KAFKA_BROKERS` is set |
| `secrets` | AES-256-GCM sealing under the installation master key; per-tenant data keys (`Keyring`) |
| `ids` | UUIDv7 ids, random secrets, URL id parsing |
| `objstore` | Writes archive objects through the S3 API to any S3-compatible server; nil when no endpoint is configured |

## Background work

`jobs.Runner` executes the job kinds that modules register. Scheduled tasks are driven by
`internal/app`.

## Data

Used by these packages (tables created in migrations owned by other modules):
`audit_entry` and `job` (`00001_platform.sql`), `tenant_key` (`00006_saas.sql`).

## Depends on

Nothing outside `core` (`auth` uses `httpx`; `kernel` ties the rest together).

## Spec gaps

None marked `TODO(spec)`.

## Tests

`secretsrc` and `config` have unit tests (fake Vault, temp files, generated certificates). The other packages are exercised by the module integration
tests: from `backend/`, `eval "$(../scripts/testdb.sh)" && go test ./internal/...`.

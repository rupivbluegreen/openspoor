# platform

Tenancy and identity. The module owns tenants, organizations, sites (GLN), licenses, users
and roles, API keys, the audit log API, 21 CFR Part 11-style electronic signatures and the
shared alert inbox that other modules write to. It also implements the user and API-key
store behind `core/auth` (login, tenant memberships, key resolution) and `CreateTenant`,
which `saas` and `ecosystem` use to provision tenants.

## Endpoints (`/api/v1`)

| Method | Path | Purpose |
|---|---|---|
| POST | `/auth/login` | Password login, returns a token (public) |
| GET | `/me` | Current principal |
| GET | `/me/tenants` | Tenants the user belongs to |
| POST | `/auth/switch` | Switch to another tenant |
| GET | `/tenant` | Current tenant |
| GET, POST, PUT | `/orgs`, `/orgs/{id}` | Organizations (admin) |
| GET, POST, PUT | `/sites`, `/sites/{id}` | Sites (admin) |
| GET, POST | `/licenses` | Licenses (admin / compliance) |
| GET, POST, PATCH | `/users`, `/users/{id}` | Users and roles (admin) |
| GET, POST, DELETE | `/api-keys`, `/api-keys/{id}` | Scoped API keys (admin / integration) |
| GET | `/audit` | Audit log |
| GET | `/audit/verify` | Verify the audit hash chain |
| GET, POST | `/esignatures` | List / record e-signatures (password re-entry, meaning, reason) |
| GET | `/alerts` | Alert inbox |
| POST | `/alerts/{id}/resolve` | Resolve an alert |

## Background work

None.

## Data

`tenant`, `organization`, `site`, `license`, `app_user`, `api_key`, `esignature`, `alert`
(migration `00001_platform.sql`, which also creates `audit_entry` and `job` for
`core/audit` and `core/jobs`).

## Depends on

`core/*`, `pkg/gs1`.

## Spec gaps

None marked `TODO(spec)`.

## Tests

From `backend/`: `eval "$(../scripts/testdb.sh)" && go test ./internal/platform`.

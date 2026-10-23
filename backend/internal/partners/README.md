# partners

Trading partner connections. The module owns the partner list, the public partner
directory (each tenant's listing), invitations (codes valid for 14 days), connection
approval and suspension, the exchange of endpoints and master data, and partner health.
Other modules read partners through `Get` and `ByGLN` inside the caller's tenant transaction
(used by `dscsa` and `link`).

## Endpoints (`/api/v1/partners`, roles admin / ops / integration / compliance)

| Method | Path | Purpose |
|---|---|---|
| GET, POST | `/partners` | List / add a manually configured partner (starts active) |
| GET | `/partners/directory` | Search the partner directory |
| GET, PUT | `/partners/listing` | This tenant's directory listing |
| GET, POST | `/partners/invitations` | List / create invitations |
| POST | `/partners/invitations/accept` | Accept an invitation (invited tenant); the inviter must then approve |
| GET, PUT | `/partners/{id}` | Get / update a partner |
| POST | `/partners/{id}/approve` | Approve a connection |
| POST | `/partners/{id}/suspend` | Suspend a connection |
| POST | `/partners/{id}/health` | Run a health check now |

## Background work

- Schedule `partners.health` (every 5 min): probes every active partner's endpoints.

## Data

`partner`, `partner_invitation`, `directory_listing` (migration `00004_partners_dscsa.sql`).

## Depends on

`core/*`, `pkg/gs1`.

## Spec gaps

None marked `TODO(spec)`. Open `TODO`: invitation e-mail delivery is not implemented; the
invitation code is returned to the inviter instead.

## Tests

No test file in this package. Partner flows are exercised by the `dscsa` and `link`
integration tests: from `backend/`, `eval "$(../scripts/testdb.sh)" && go test ./internal/dscsa ./internal/link`.

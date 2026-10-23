# partners

Trading partner connections. The module owns the partner list, the public partner
directory (each tenant's listing), invitations (codes valid for 14 days), connection
approval and suspension, the exchange of endpoints and master data, partner health, the
network directory (opt-in public profiles with GLNs, company prefixes, licenses and a contact
for connection requests; verification marks set by operators) and partner onboarding
(checklists from templates, test runs recorded by the link test harness, readiness score 0-100
= share of checklist items satisfied; a test item counts when the latest run on its channel passed).
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
| GET | `/partners/directory/requests` | Incoming connection requests |
| POST | `/partners/directory/{tenantId}/connect` | Ask a published tenant to connect (alert in their inbox) |
| GET | `/partners/onboarding/templates` | Onboarding templates |
| GET, POST | `/partners/{id}/onboarding` | Onboarding state and score / start from a template |
| PATCH | `/partners/{id}/onboarding/items/{itemId}` | Tick a manual checklist item |
| POST | `/directory/{tenantId}/verification` | Set verification marks (operators only) |
| GET | `/directory`, `/directory/{tenantId}` | Public directory search / profile (no auth, rate-limited 5 req/s per client, published fields only, no endpoints) |

## Background work

- Schedule `partners.health` (every 5 min): probes every active partner's endpoints.

## Data

`partner`, `partner_invitation`, `directory_listing` (migration `00004_partners_dscsa.sql`);
profile columns, `directory_connection_request`, `partner_onboarding`, `partner_onboarding_item`,
`partner_test_run` (migration `00015_link_network.sql`).

## Depends on

`core/*`, `pkg/gs1`.

## Spec gaps

`TODO(spec)` in `onboarding.go`: the DSCSA and EU FMD checklist templates are integration steps,
to be confirmed against the applicable guidance. Open `TODO`: invitation e-mail delivery is not implemented; the
invitation code is returned to the inviter instead.

## Tests

From `backend/`: `eval "$(../scripts/testdb.sh)" && go test ./internal/partners ./internal/link`.

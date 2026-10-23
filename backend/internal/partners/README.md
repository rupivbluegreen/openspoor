# partners

Trading partner connections. The module owns the partner list, the public partner
directory (each tenant's listing), invitations (codes valid for 14 days), connection
approval and suspension, the exchange of endpoints and master data, partner health, the
network directory (opt-in public profiles with GLNs, company prefixes, licenses and a contact
for connection requests; verification marks set by operators) and partner onboarding
(checklists from templates, test runs recorded by the link test harness, readiness score 0-100
= share of checklist items satisfied; a test item counts when the latest run on its channel passed).
Other modules read partners through `Get` and `ByGLN` inside the caller's tenant transaction
(used by `dscsa` and `link`); `LoadAS2` / `LoadAS2Certs` (both certificates of a rotation) and `ByAS2ID` give `dscsa` the AS2 profile and the
partner behind an AS2 identifier; `AS2Overview` feeds the certificate overview.

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
| GET, PUT, DELETE | `/partners/{id}/as2` | AS2 profile of the connection: the partner's certificate (sealed with the tenant key; subject, notBefore, notAfter and fingerprint stored), `sign`, `encrypt`, `mdn` (`sync`/`async`), `mdnUrl` and the options below. PUT / DELETE: admin or integration; needs `endpoints.as2Id`. Audited (`partner.as2.set`, `partner.as2.delete`), the audit entry names the certificate by fingerprint. Options left out of a PUT keep their value |
| PUT | `/partners/{id}/as2/options` | Change only the options: `signatureAlgorithm` (sha-256 default, sha-384, sha-512; sha-1 only with `legacy`), `mdnSignatureAlgorithm`, `encryption` (aes-256-cbc default, aes-128-cbc), `compress` (off), `legacy` (explicit flag admitting SHA-1: audited as `partner.as2.legacy.set`, raises `dscsa.as2_legacy`, shown in the overview). Admin or integration |
| PUT, DELETE | `/partners/{id}/as2/next` | Rotation: hold the partner's next certificate with `switchAt` (absent = now; past dates refused; the certificate must differ from the current one, be unexpired and valid on that date). Both verify the partner's signatures; we encrypt to the next one from `switchAt`. DELETE cancels. Admin or integration; audited (`partner.as2.next.set` / `.cancel`) |
| GET | `/partners/directory/requests` | Incoming connection requests |
| POST | `/partners/directory/{tenantId}/connect` | Ask a published tenant to connect (alert in their inbox) |
| GET | `/partners/onboarding/templates` | Onboarding templates |
| GET, POST | `/partners/{id}/onboarding` | Onboarding state and score / start from a template |
| PATCH | `/partners/{id}/onboarding/items/{itemId}` | Tick a manual checklist item |
| POST | `/directory/{tenantId}/verification` | Set verification marks (operators only) |
| GET | `/directory`, `/directory/{tenantId}` | Public directory search / profile (no auth, rate-limited 5 req/s per client, published fields only, no endpoints) |

## Background work

- Schedule `partners.health` (every 5 min): probes every active partner's endpoints.
- Schedule `partners.as2_certs` (every 30 min): promotes a partner's next AS2 certificate once its switch date passed (audited as the system) and raises one `dscsa.as2_cert_expiring` alert per certificate and stage (60, 30, 7 days, expired).

## Data

`partner`, `partner_invitation`, `directory_listing` (migration `00004_partners_dscsa.sql`); `partner_as2` (migration `00023_as2.sql`; certificate details, the next certificate and the options in `00029_as2_certs_options.sql`, with `as2_cert_alert`);
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

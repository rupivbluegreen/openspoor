# saas

Runs the same binary as a self-hostable multi-tenant service. The module owns self-service
sign-up and tenant provisioning, subdomain and custom-domain routing (domain ownership is
proven with a `_openspoor.<domain>` TXT record; an on-demand TLS "ask" endpoint answers 200
only for tenant domains), per-tenant branding, plans, quotas and feature flags (an OpenFeature
provider that resolves flags from the tenant's plan; self-hosted installs get every feature),
usage metering and billing push, the operator console APIs with consented, time-boxed and
audited support access, region pinning, and tenant export and deletion (GDPR). Deletion
destroys the tenant's data key first (crypto-shredding sealed secrets), then deletes all rows;
the hash-chained audit trail is retained.

It meters captured EPCIS events from the `epcis.accepted` bus event and installs the capture
quota gate on `epcis`. Billing is chosen with `OPENSPOOR_BILLING`; the default (`none`, for
self-hosted installs) reports nothing. See `billing.go` for the drivers.

## Endpoints (`/api/v1`)

| Method | Path | Purpose |
|---|---|---|
| POST | `/saas/signup` | Self-service sign-up (public) |
| GET | `/saas/branding` | Branding for the requesting host (public) |
| GET | `/saas/plans` | Plans (public) |
| GET | `/saas/tls-ask` | On-demand TLS check for a domain (public) |
| GET | `/tenant/usage` | Usage against quotas |
| GET | `/tenant/features` | Feature flags for this tenant |
| GET, PUT | `/tenant/branding` | Get / set branding (set: admin) |
| POST | `/tenant/domain`, `/tenant/domain/verify` | Set / verify a custom domain (admin) |
| POST | `/tenant/support-access` | Consent to operator support access (admin) |
| GET, POST | `/tenant/export` | Export all tenant data as JSON (admin) |
| DELETE | `/tenant` | Delete the tenant (admin re-authenticates and confirms) |
| GET | `/operator/tenants` | List tenants (operator) |
| GET | `/operator/hub/analytics` | Market analytics across reporting organisations (`days`, default 30); hub mode only (404 elsewhere), operator only. Shown in the operator console as "Market analytics" |
| PATCH | `/operator/tenants/{id}` | Update plan, status, region or quotas, with a reason (operator) |
| POST | `/operator/tenants/{id}/impersonate` | Short-lived support token, only with consent (operator) |

## Background work

- Schedule `saas.billing` (hourly): pushes unbilled usage to the billing driver (no-op for `none`).
- Bus subscription `epcis.accepted`: adds to the monthly `epcis_events` usage counter.

## Data

`usage_counter`, `tenant_key` (migration `00006_saas.sql`; `tenant_key` is read by
`core/secrets`). Tenant settings (plan, quotas, domain, branding, support access) are
columns on `tenant`.

## Depends on

`core/*`, `platform` (tenant creation and lookup).

## Spec gaps

None marked `TODO(spec)`. One billing driver is a reserved stub (plain `TODO` in `billing.go`).

## Tests

From `backend/`: `eval "$(../scripts/testdb.sh)" && go test ./internal/saas`.

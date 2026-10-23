# dscsa

US DSCSA interoperability. The module owns outbound shipments and their Transaction
Information / Transaction Statement (TI/TS) EPCIS documents, partner exchange (HTTPS; AS2 is
an interface boundary for an external gateway, see ADR-007; a `local` transport delivers
between tenants on the same node), reconciliation of inbound TI/TS with the ASN, the
Verification Router Service (VRS) requester and responder (GS1 Lightweight Messaging), a VRS
lookup directory and record retention (six years by default, per-tenant overrides).

## Endpoints (`/api/v1`)

| Method | Path | Purpose |
|---|---|---|
| GET, POST | `/shipments` | List / create outbound shipments |
| POST | `/shipments/inbound` | Receive an inbound TI/TS document |
| GET | `/shipments/{id}` | Get a shipment |
| POST | `/shipments/{id}/tits` | Build the TI/TS document |
| POST | `/shipments/{id}/send` | Send TI/TS to the partner |
| POST | `/shipments/{id}/reconcile` | Reconcile inbound TI/TS with the ASN |
| GET, POST | `/vrs/requests` | List / send VRS verification requests |
| GET, POST | `/vrs/directory` | List / upsert VRS lookup directory entries |
| DELETE | `/vrs/directory/{id}` | Remove a directory entry |
| GET | `/retention` | Retention policies |
| PUT | `/retention/{class}` | Set a retention period (admin / compliance) |
| POST | `/vrs/verify` | VRS responder (public, no auth) |

Shipment and VRS routes need the admin, ops, compliance or integration role.

## Background work

- Schedule `dscsa.retention` (every 24 h): deletes EPCIS events and VRS requests older than
  each tenant's retention period (never less than six years).

## Data

`shipment`, `vrs_directory`, `vrs_request`, `retention_policy` (migration
`00004_partners_dscsa.sql`). Outbound and inbound TI/TS are captured into the EPCIS repository.

## Depends on

`core/*`, `epcis`, `masterdata`, `partners`, `trace`, `pkg/epcis`, `pkg/gs1`.

## Spec gaps

- TI/TS header: "align header vocabulary attribute URIs exactly with the current GS1 US
  Healthcare 'Applying GS1 Standards for DSCSA' guideline before production use."
- VRS: "field names follow the GS1 Lightweight Messaging Standard ... confirm against the
  current release before certification."

## Tests

From `backend/`: `eval "$(../scripts/testdb.sh)" && go test ./internal/dscsa`.

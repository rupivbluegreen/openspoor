# connectors

Country regulatory connectors. The module owns the connector catalog, per-tenant connector
instances, the mapping from captured EPCIS events to regulatory reports, submission with
retries and backoff, status polling, the regulatory alert inbox (parsed reason, affected
serials, suggested fix, resubmit) and the versioned YAML rule packs in `backend/rulepacks`
(`eu-fmd`, `eu-eudr`, `sa-rsd`, `us-dscsa`, `us-fsma204`).

Connectors implement the interface from the separate `connectors-sdk` Go module.
Sub-packages:

- `eufmd` — EU FMD pack data upload and status changes (`eu-fmd-emvs`).
- `sarsd` — Saudi SFDA RSD notifications (`sa-rsd`).
- `mockhub` — in-memory mock hubs reachable at `http://<name>.mock.local` through an
  in-process transport, for development, demos and contract tests.

## Endpoints (`/api/v1`, roles admin / compliance / integration)

| Method | Path | Purpose |
|---|---|---|
| GET | `/connectors` | Connector catalog |
| GET, POST | `/connectors/instances` | List / create tenant instances |
| PUT | `/connectors/instances/{id}` | Update an instance |
| POST | `/connectors/instances/{id}/submit` | Queue a capture for this instance manually |
| GET | `/reports`, `/reports/{id}` | List / get regulatory reports |
| POST | `/reports/{id}/resubmit` | Resubmit a failed report |
| GET | `/rulepacks`, `/rulepacks/{id}` | List / get rule packs |
| POST | `/rulepacks/evaluate` | Evaluate rule packs against input |

## Background work

Jobs (on `core/jobs`):
- `connectors.dispatch` — enqueued by `epcis` on every capture; maps events to reports.
- `connectors.submit` — sends a report to the authority, with retries.
- `connectors.poll` — polls submission status.

## Data

`connector_instance`, `regulatory_report` (migration `00005_connectors.sql`). Failures are
raised as rows in the platform `alert` table.

## Depends on

`core/*`, `masterdata`, `trace`, `rulepacks`, `pkg/epcis`, `connectors-sdk`.

## Spec gaps

- `eufmd`: "the EU Hub interface specification is distributed by EMVO to onboarded partners
  and is not public" — paths and codes are the mock hub's; the base URL is a placeholder.
- `sarsd`: "RSD exposes SOAP web services documented in SFDA's integration guide" — this
  connector speaks JSON to the mock hub; spec version and service URL are open.

## Tests

From `backend/`: `eval "$(../scripts/testdb.sh)" && go test ./internal/connectors/...`.
`contract_test.go` runs against the mock hubs without a database.

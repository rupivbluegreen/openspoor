# migration

Moving off a legacy track-and-trace system. The module imports serial numbers and their
history from CSV exports or EPCIS (1.2 XML or 2.0) documents, and runs shadow mode: feed Open
Spoor the same Level 3 input as the legacy system, then compare the legacy system's EPCIS
output with Open Spoor's serial state and report every mismatch.

CSV import header: `gtin,serial[,lot,expiry,status,parent,event_time]`. Rows become
commissioning, aggregation and decommissioning events (source `migration`). Status values:
`active` (default), `dispensed`, `destroyed`, `decommissioned`, `recalled`, `expired`.
Imported serials need not come from Open Spoor pools, but GTIN and double-commission checks
still apply.

## Endpoints (`/api/v1`, roles admin / serialization / integration)

| Method | Path | Purpose |
|---|---|---|
| POST | `/migration/serials` | Import serials from CSV |
| POST | `/migration/epcis` | Import legacy EPCIS history |
| POST | `/shadow/runs` | Compare a legacy EPCIS document (`label`, `legacyDocument`) with current state |
| GET | `/shadow/runs`, `/shadow/runs/{id}` | List / get shadow run reports |

## Background work

None.

## Data

`shadow_run` (migration `00011_migration.sql`). Imports are stored through the EPCIS
repository.

## Depends on

`core/*`, `epcis` (capture), `trace` (final-state computation), `pkg/epcis`, `pkg/gs1`.

## Spec gaps

None marked `TODO(spec)`.

## Tests

From `backend/`: `eval "$(../scripts/testdb.sh)" && go test ./internal/migration`.

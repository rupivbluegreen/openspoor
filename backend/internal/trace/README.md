# trace

Read projections over the EPCIS event store. The module maintains the current state of every
serial (status, disposition, location, lot, expiry, parent) and the aggregation tree, and
serves serial history and parent/child lookups. Projection never moves a serial back in time:
a row changes only when the incoming event is not older than its last event. `FinalStates`
computes the same end state from a list of events (used by `migration` shadow runs).

## Endpoints (`/api/v1/trace`, roles admin / serialization / ops / compliance / integration)

| Method | Path | Purpose |
|---|---|---|
| GET | `/trace/serials/*` | State, ancestors, direct children and event history for an identifier |
| GET | `/trace/tree/*` | Aggregation tree below an identifier (up to 6 levels) |
| GET | `/trace/stats` | Event and capture counts |
| GET | `/trace/state` | List serial state, filtered by `gtin`, `status`, `lot` |

## Background work

- Job `trace.project` (enqueued by `epcis` in every capture transaction): applies the
  capture's events to `serial_state`, marks the capture completed, then publishes the bus event `epcis.captured`.

## Data

`serial_state` (hash-partitioned into 8 partitions, migration `00003_epcis.sql`). Reads
`epcis_event` and updates `epcis_capture` status; both belong to `epcis`.

## Depends on

`core/*`, `pkg/epcis`.

## Spec gaps

None marked `TODO(spec)`.

## Tests

No test file in this package. Projection and lookups are covered by the `epcis`, `dscsa`
and `migration` integration tests: from `backend/`,
`eval "$(../scripts/testdb.sh)" && go test ./internal/epcis ./internal/dscsa ./internal/migration`.

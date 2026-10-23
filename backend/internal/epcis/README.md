# epcis

The EPCIS 2.0 repository. The module owns capture of JSON-LD and XML documents (EPCIS 1.2 XML
is read too), validation against the CBV, master data and serial pools (unknown GTINs and
double commissioning are rejected), atomic storage (every event in a document is stored, or
none and the per-event errors are returned), the standard query interface, named queries and
webhook subscriptions with HMAC-signed deliveries. Parsing and validation live in
`pkg/epcis`; this module adds persistence and tenant rules.

Each capture enqueues follow-up jobs in the same transaction (transactional outbox): the
trace projection, subscription notification and any kinds registered with `OnCapture`
(the app registers `connectors.dispatch`). An optional `Gate` (set by `saas`) can refuse a
capture over quota.

## Endpoints (`/api/v1/epcis`)

| Method | Path | Purpose |
|---|---|---|
| POST | `/capture` | Capture a document; 202 with a `Location` to the capture |
| GET | `/capture`, `/capture/{id}` | List / get captures |
| GET | `/events` | Query events (EPCIS query parameters) |
| GET | `/events/{eventID}` | Get one event |
| GET, POST | `/queries` | List / create named queries |
| GET | `/queries/{name}/events` | Run a named query |
| GET, POST | `/subscriptions` | List / create webhook subscriptions |
| DELETE | `/subscriptions/{id}` | Delete a subscription |

## Background work

Jobs handled here:
- `epcis.notify` — finds subscriptions matching a capture and enqueues deliveries.
- `epcis.deliver` — POSTs the document to the subscriber (up to 10 attempts).

Jobs enqueued for others: `trace.project` (handled by `trace`). Bus event `epcis.accepted`
after each capture.

- Schedule `epcis.archive` (hourly, only when `OPENSPOOR_S3_ENDPOINT` is set): copies
  captures older than `OPENSPOOR_ARCHIVE_AFTER_DAYS` (default 90) to S3-compatible storage as
  gzipped EPCIS 2.0 JSON-LD, up to 50 per run, and records `epcis_capture.archive_key`.
  Events stay in PostgreSQL.

## Data

`epcis_capture`, `epcis_event` (monthly partitions created on demand, plus a default partition),
`epcis_named_query`, `epcis_subscription` (migration `00003_epcis.sql`).

`trace` reads `epcis_event` for the mass balance, the recall trace-forward and the signed checkpoints
(`openspoor verify-checkpoint`); this module's tables are not changed for them.

## Depends on

`core/*` (including `core/objstore`), `masterdata`, `serialization`, `pkg/epcis`, `pkg/gs1`.

## Spec gaps

None marked `TODO(spec)` in this module (see `pkg/epcis` for the event hash).

## Tests

From `backend/`: `eval "$(../scripts/testdb.sh)" && go test ./internal/epcis`.
The capture benchmark runs only with `OPENSPOOR_BENCH=1` (see `make bench`).

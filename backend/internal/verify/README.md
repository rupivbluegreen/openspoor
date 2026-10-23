# verify

Public, unauthenticated surfaces: pharmacy/consumer "scan and verify" and the EPCIS
Playground (validate, convert, visualize). All routes are rate-limited per client IP
(5 requests/s, burst 30).

A scan decodes the code, finds the brand owner on this node (the tenant with a serial pool
for the GTIN, else any tenant with the product) and checks the pack against that tenant's
records. Results: `verified`, `unknown_product`, `not_found`, `suspect` (no serial, batch or
expiry mismatch), `recalled`, `decommissioned`, `expired`. Any result other than `verified`
raises a deduplicated alert for the brand owner.

## Endpoints (`/api/v1`, public)

| Method | Path | Purpose |
|---|---|---|
| POST | `/verify/scan` | Verify a scanned pack (`{"code": ...}`) |
| POST | `/playground/validate` | Validate an EPCIS document and report field errors |
| POST | `/playground/convert` | Convert between EPCIS JSON-LD and XML |
| POST | `/playground/visualize` | Build a node/edge graph of a document's objects and events |

The module has no authenticated routes.

## Background work

None.

## Data

No tables of its own. Reads `product`, `serial_pool` and `serial_state`; writes `alert`.

## Depends on

`core/*`, `masterdata`, `serialization` (code parsing), `trace` (serial state), `pkg/epcis`,
`pkg/gs1`.

## Spec gaps

None marked `TODO(spec)`.

## Tests

From `backend/`: `eval "$(../scripts/testdb.sh)" && go test ./internal/verify`.

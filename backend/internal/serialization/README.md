# serialization

Serial number issuance. The module owns serial pools per GTIN and market, the idempotent
allocation API for Level 3 systems, GS1 DataMatrix and Digital Link encoding and decoding,
commissioned-serial bitmaps (used by `epcis` to detect double commissioning) and pool
exhaustion alerts.

Pools run in `random` or `sequential` mode. Random mode encrypts a counter with FF1
format-preserving encryption (NIST SP 800-38G) in the pool alphabet, so serials are unique by
construction without looking up earlier ones. The FF1 key is sealed with the tenant key. The
default alphabet omits `I` and `O`.

## Endpoints (`/api/v1`)

| Method | Path | Purpose |
|---|---|---|
| GET, POST | `/serial-pools` | List / create pools (admin / serialization / integration) |
| GET | `/serial-pools/{id}` | Pool detail and usage |
| POST | `/serial-pools/{id}/allocate` | Allocate serials; requires an `Idempotency-Key` header |
| POST | `/codes/encode` | Encode a pack as element string, Digital Link, URN and optional DataMatrix PNG |
| POST | `/codes/decode` | Decode a scanned code (element string, HRI text, Digital Link or SGTIN URN) |

## Background work

None. When a pool drops below its low-watermark it raises a `serialization.pool_low` alert
and publishes the same bus event.

## Data

`serial_pool` (including the `commissioned` bitmap), `serial_allocation` (migration
`00002_masterdata_serialization.sql`).

## Depends on

`core/*`, `masterdata` (product lookup), `pkg/epcis`, `pkg/gs1`.

## Spec gaps

- DataMatrix image: GS1 DataMatrix encoding (FNC1 start codeword, FNC1 separators); verify with a scanner before printing.

## Tests

`generator_test.go` needs no database: from `backend/`, `go test ./internal/serialization`.
API behaviour is covered by integration tests in other modules after
`eval "$(../scripts/testdb.sh)"`.

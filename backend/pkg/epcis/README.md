# pkg/epcis

An Apache-2.0 implementation of GS1 EPCIS 2.0 documents, independent of Open Spoor internals
(it depends only on `pkg/gs1`). The `internal/epcis` repository and the public playground are
built on it.

## What it covers

| Area | API |
|---|---|
| Model | `Document`, `Body`, `Event` (object, aggregation, transformation, association, transaction events), quantities, sources/destinations, sensor elements, error declarations |
| Reading | `Parse` (by content type, or sniffs the first byte), `ParseJSON` (JSON-LD; a bare event is accepted), `ParseXML` (EPCIS 2.0 and 1.2 XML; extension wrappers flattened) |
| Writing | `WriteJSON` (JSON-LD), `WriteXML`, `NewDocument` |
| CBV | Standard business steps, dispositions and other CBV 2.0 vocabularies; `Short` / `URN` convert between bare terms and URN forms |
| Validation | `Normalize` (bare CBV terms, time zone offset), `Validate`, `ValidateDocument`, `CheckEPC`; errors are `FieldError` per event |
| Identifiers | `Canonical` / `CanonicalAll` map EPC URNs and Digital Links (any domain) to one `https://id.gs1.org` key; `Key` splits it into GTIN, serial, lot |
| Event IDs | `HashID` derives a deterministic event ID from business content (idempotent capture) |
| Helpers | `ParallelFor` runs work across GOMAXPROCS workers |

Unknown extension fields are preserved on round trips.

## Spec gaps

- `HashID`: "follows the structure of the GS1 CBV 2.0 event hash ... but is not yet
  byte-for-byte the GS1 canonical algorithm; it is therefore tagged ver=openspoor1, not
  ver=CBV2.0."

## Tests

From `backend/`: `go test ./pkg/epcis` (no database). Tests include the GS1 example
documents, EPCIS 1.2 XML reading, validation errors and extension preservation.

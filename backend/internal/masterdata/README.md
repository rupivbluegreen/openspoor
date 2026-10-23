# masterdata

Product master data. The module owns products (GTIN, NDC, market codes, dosage form, strength,
pack size, vertical), per-market packaging hierarchy templates and CSV import. Other modules
read products through the exported lookup functions `ByGTIN`, `ByID` and `KnownGTINs`, which
run inside the caller's tenant transaction.

## Endpoints (`/api/v1/products`, roles admin / serialization / compliance)

| Method | Path | Purpose |
|---|---|---|
| GET, POST | `/products` | List / create products |
| POST | `/products/import` | CSV import; valid rows are applied, invalid rows reported by line |
| GET, PUT | `/products/{id}` | Get / update a product |
| GET | `/products/{id}/packaging` | Packaging templates for all markets |
| GET, PUT | `/products/{id}/packaging/{market}` | Get / set the template for one market |

CSV header: `gtin,name,ndc,company_prefix_length,target_markets,dosage_form,strength,pack_size,description,vertical`.

## Background work

None.

## Data

`product`, `packaging_template` (migration `00002_masterdata_serialization.sql`).

## Depends on

`core/*`, `pkg/gs1`.

## Spec gaps

None marked `TODO(spec)`.

## Tests

No test files in this package. It is covered by other modules' integration tests (for
example `go test ./internal/epcis ./internal/serialization`) run from `backend/` after
`eval "$(../scripts/testdb.sh)"`.

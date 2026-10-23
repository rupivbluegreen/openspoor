# food

The food vertical. The module owns lot-level traceability with traceability lot codes
(TLCs), FSMA 204 critical tracking events (CTEs) with their key data elements, transformations
(input lots consumed into a new output lot), a farm/plot registry with GeoJSON, the FSMA 204
sortable spreadsheet export, recalls and EUDR due diligence statements (DDS). CTEs are mirrored
into the EPCIS repository as class-level events, so food and pharma share trace, alerts and
agents.

Recalls follow forward lineage from the recalled lots, find every customer they were shipped
to and pharma serials in the recalled batches, mark lots recalled and notify customers
(partners on the same node get an alert).

## Endpoints (`/api/v1/food`; tenant must have the food vertical; roles admin / ops / compliance / integration)

| Method | Path | Purpose |
|---|---|---|
| GET, POST | `/lots` | List / create lots |
| GET | `/lots/{tlc}/trace` | Backward and forward lineage of a lot |
| GET, POST | `/cte` | List / record critical tracking events |
| POST | `/transformations` | Record a transformation CTE |
| GET, POST | `/plots` | List / register farm plots |
| GET | `/fsma204/export` | FSMA 204 sortable spreadsheet (CSV) |
| GET, POST | `/recalls` | List / start recalls |
| GET | `/recalls/{id}` | Recall detail with affected lots and notifications |
| POST | `/recalls/{id}/close` | Close a recall |
| GET, POST | `/eudr/dds` | List / create due diligence statements from registered plots |
| GET | `/eudr/dds/{id}` | Get a statement |
| POST | `/eudr/dds/{id}/submit` | Mark submitted with a local reference (stub) |

## Background work

None.

## Data

`food_lot`, `food_cte`, `food_lineage`, `farm_plot`, `recall`, `recall_notification`,
`eudr_dds` (migration `00007_food.sql`).

## Depends on

`core/*`, `epcis` (CTE mirroring), `pkg/epcis`, `pkg/gs1`.

## Spec gaps

- FSMA 204 export: "align header names and order with FDA's published sortable spreadsheet
  template."
- EUDR: "map the payload to the EU Information System (TRACES) DDS schema when submitting";
  submission is stubbed.

## Tests

From `backend/`: `eval "$(../scripts/testdb.sh)" && go test ./internal/food`.

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

The one-up/one-down report serves Regulation (EC) No 178/2002 Article 18: per lot, the
immediate suppliers (receiving events: supplier GLN and name, date, quantity, lot, reference
document) and immediate customers (shipping events). Transformations are followed inside the
operator: inputs of an output lot are its one-up (`transformation_input`), and receipts of those
inputs are reached with `viaLot` set; likewise outputs and their shipments for one-down. The
date range filters the events. The Regulation prescribes no format or columns, which the
export notes say (CSV `#` comment lines, JSON `notes`). Authority requests assemble the report
for a set of lots, store it as generated (with its SHA-256), and record every download (who,
when, format) on the request and in the audit trail.

The traceability plan (`plan.go`) is the document 21 CFR 1.1315 asks a covered person to keep. Only
the five elements read from the public rule text are asserted: (a)(1) record-keeping procedures,
(a)(2) procedures to identify listed foods handled, (a)(3) how traceability lot codes are assigned,
(a)(4) point of contact, (a)(5) farm map for farms. Each section is free text; the structured
helpers (products with a Food Traceability List category, lot-code pattern/source/example seen in
`food_lot`, contact fields, selection of registered plots with a schematic SVG overview) are
labelled as helpers in every export. Saving never edits a row: it inserts the next version
(numbered per tenant or per site) with who, when and a required reason, and moves the current
flag. Products carry no food category column; the category is stored on the plan entry, so the
master data is untouched. The review reminder is a per-tenant setting, off by default, with no
legal source (the interval is a software convention).

Certificates (halal, origin, organic, …) are the supplier compliance hub's records
(`supplier_record`, reached through the `CertificateStore` interface that `internal/app` wires,
since food may only depend on `epcis`). Food links them to a lot (TLC), a product or a site
(GLN). A lot is covered by certificates linked to the lot, its product or one of its sites (TLC
source, location, ship-from of its receipts), and by hub certificates held for a supplier whose
GLN is one of those sites. Certificate kinds are free text; no certification body rules are
modelled. Certificate references appear in the one-up/one-down rows and, with
`certificates=true`, in the FSMA 204 export's "Additional KDEs" JSON (they are not FSMA KDEs, so
they are off by default).

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
| GET | `/traceability-plan` | Current traceability plan (`siteId` optional) with editor helpers, review status and completeness hints |
| GET | `/traceability-plan/versions` | All saved versions, newest first (who, when, why, current flag) |
| PUT | `/traceability-plan` | Save a new version (`content`, required `reason`, optional `siteId`); admin / ops / compliance only |
| GET | `/traceability-plan/export` | `format=json\|html\|md`, optional `version`, `siteId`; each download is audited |
| GET, PUT | `/traceability-plan/settings` | Per-tenant review reminder (off by default; PUT: admin / ops / compliance) |
| GET | `/one-up-one-down` | One-up/one-down report (`tlc` repeatable or comma-separated, `productId`, `gtin`, `from`, `to`; `format=csv\|json` downloads and is audited) |
| GET, POST | `/one-up-one-down/authority-requests` | List / assemble a report for a competent authority |
| GET | `/one-up-one-down/authority-requests/{id}` | Request with its stored report |
| GET | `/one-up-one-down/authority-requests/{id}/export` | Download as CSV or JSON (recorded on the request and audited) |
| GET, POST | `/certificates` | List linked certificates (`targetType`, `target`, `kind`, `status`) / create a hub certificate with links |
| POST | `/certificates/{id}/links` | Link an existing hub certificate to a lot, product or site |
| DELETE | `/certificates/links/{linkId}` | Remove a link (the certificate stays in the hub) |
| GET | `/lots/{tlc}/certificates` | Certificates covering a lot, with validity and how they cover it |

Viewer may read (including downloads); writes need one of the roles above.

## Background work

- Schedule `food.cert_expiry` (every 6 h): an alert (`food.certificate_expiring`; warning when
  expiring within 30 days, error when expired) per linked certificate, listing the active lots it
  covers; deduplicated per certificate, expiry date and state, and audited as `system`.

## Data

`food_lot`, `food_cte`, `food_lineage`, `farm_plot`, `recall`, `recall_notification`,
`eudr_dds` (migration `00007_food.sql`); `food_certificate_link`, `food_authority_request` and
the `supplier_record.scope` column (migration `00016_food_oneup_certificates.sql`);
`food_traceability_plan` and `food_traceability_plan_setting` (migration `00020_traceability_plan.sql`).

## Depends on

`core/*`, `epcis` (CTE mirroring), `pkg/epcis`, `pkg/gs1`. `internal/app` provides the
`CertificateStore` over the supplier compliance hub's `supplier_record`.

## Spec gaps

- FSMA 204 export: "align header names and order with FDA's published sortable spreadsheet
  template."
- Traceability plan: `TODO(spec)` confirm the element wording against the current eCFR text, the
  Food Traceability List categories offered as suggestions, and whether plan versions must be kept
  for a set period (all are kept) or reviewed on a schedule (none is asserted).
- EUDR: "map the payload to the EU Information System (TRACES) DDS schema when submitting";
  submission is stubbed.
- One-up/one-down: the Commission's guidance on Articles 11, 12, 14, 17, 18, 19 and 20 of
  Regulation (EC) No 178/2002 recommends further information (such as supplier and customer
  addresses); only fields captured on CTEs are included. Confirm against the current guidance
  and, for food of animal origin, Implementing Regulation (EU) No 931/2011.

## Tests

From `backend/`: `eval "$(../scripts/testdb.sh)" && go test ./internal/food`.

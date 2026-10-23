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
| GET | `/eudr/dds/{id}/checklist` | Completeness checklist (`ok` / `missing` / `warning` / `not_applicable` items; `complete` when nothing is missing) |
| GET, PUT | `/eudr/settings` | The tenant's DDS role: `operator` (creates statements) or `downstream_trader` (records the upstream reference); PUT audited |
| GET | `/eudr/dates` | Application dates, labelled "unverified, check the Official Journal" until a person confirms them |
| PUT, DELETE | `/eudr/dates/{key}` | Set (date, note, verified) or reset a reference date (`large_medium_operators`, `micro_small_operators`); audited |
| GET | `/eudr/plots/validation` | Validation report for every plot with machine-readable reasons and overlaps |
| GET | `/eudr/plots/{id}/validation` | The report of one plot |
| GET | `/cold-chain/excursions` | Sensor excursions with their disposition (`disposition=open\|quarantine\|release\|destroy`) |
| GET | `/cold-chain/excursions/{id}` | Excursion report: readings, min/max/mean, mean kinetic temperature (`activationEnergyKJ` optional), decisions, lots |
| GET | `/cold-chain/excursions/{id}/report` | Download as `format=json\|csv`; every download is audited |
| POST | `/cold-chain/excursions/{id}/disposition` | `quarantine`, `release` or `destroy` with `reason`; release and destroy need `signatureId` (e-signature, entity `food_excursion`); admin / ops / compliance |
| GET | `/cold-chain/mkt` | Mean kinetic temperature of a device window (`deviceId`, `from`, `to`, optional `activationEnergyKJ`) |
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

## EUDR readiness

- **Role.** `food_eudr_setting.dds_role` is `operator` (default) or `downstream_trader`. An operator
  builds a statement from registered plots. A downstream trader records a reference to the upstream
  statement (`upstreamReference`, optional `upstreamOperator`, no plots); the record has
  `kind=upstream_reference`, status `referenced` and cannot be submitted. `TODO(spec)`: secondary
  sources say the amended regime exempts downstream traders; the software does not assert it as law.
- **Dates.** 2026-12-30 (large and medium) and 2027-06-30 (micro and small) are defaults from
  secondary sources, always shown with the status "unverified, check the Official Journal" until a
  tenant edits and confirms them.
- **Plot validation** (`eudr_geo.go`, pure functions): closure, ring length, winding (RFC 7946),
  self-intersection, coordinate range, decimals (working default 6), area sanity and mismatch,
  point-versus-polygon by size (working default 4 ha, cattle exempt; `TODO(spec)`), overlap between
  a tenant's plots (boundary crossing or containment of outer rings; the overlap area is not
  computed). Each reason has a stable `code`, a `severity` (`error`, `warning`, `info`) and a
  message; the status is `invalid` when any error is present.
- **Checklist.** Covers the fields this software holds and the plot reports; it is not a legal
  assessment (`TODO(spec)`: Article 9 / Annex II elements).

## Cold chain

- **Mean kinetic temperature** uses the Haynes formula; the default activation energy is
  83.144 kJ/mol (`DefaultActivationEnergyKJ`, a documented convention). `TODO(spec)`: product-specific
  values. Readings are weighted equally; the result says whether sampling was even.
- **Dispositions** are append-only rows in `food_excursion_disposition`; the latest is current.
  Allowed: open to any of quarantine / release / destroy, quarantine to release / destroy; release
  and destroy are final. Quarantine, release and destroy move the named lots (`tlcs`) to
  `quarantined`, back to their prior status, or `destroyed`. Release and destroy require the
  caller's e-signature on the excursion, given after the last decision and usable once. Every
  decision and every report download is audited.
- Excursions and readings are written by the ecosystem module (`sensor_excursion`,
  `sensor_reading`); this package reads them and leaves their status alone.

## Background work

- Schedule `food.cert_expiry` (every 6 h): an alert (`food.certificate_expiring`; warning when
  expiring within 30 days, error when expired) per linked certificate, listing the active lots it
  covers; deduplicated per certificate, expiry date and state, and audited as `system`.

## Data

`food_lot`, `food_cte`, `food_lineage`, `farm_plot`, `recall`, `recall_notification`,
`eudr_dds` (migration `00007_food.sql`); `food_certificate_link`, `food_authority_request` and
the `supplier_record.scope` column (migration `00016_food_oneup_certificates.sql`);
`food_traceability_plan` and `food_traceability_plan_setting` (migration `00020_traceability_plan.sql`);
`food_eudr_setting`, `food_eudr_reference_date`, `food_excursion_disposition` and the
`eudr_dds.kind` / `upstream_reference` / `upstream_operator` columns (migration `00028_food_eudr_coldchain.sql`).

## Depends on

`core/*`, `epcis` (CTE mirroring), `pkg/epcis`, `pkg/gs1`. `internal/app` provides the
`CertificateStore` over the supplier compliance hub's `supplier_record`.

## Spec gaps

- FSMA 204 export: "align header names and order with FDA's published sortable spreadsheet
  template." The template is not in this repository and could not be verified, so the columns are
  unchanged; the open points (headers, row granularity, event vocabulary, date format, file
  format) are listed in `docs/OPEN_QUESTIONS.md`.
- Traceability plan: `TODO(spec)` confirm the element wording against the current eCFR text, the
  Food Traceability List categories offered as suggestions, and whether plan versions must be kept
  for a set period (all are kept) or reviewed on a schedule (none is asserted).
- EUDR readiness and cold chain: see `docs/OPEN_QUESTIONS.md` (DDS roles, application dates,
  geolocation thresholds, statement elements, activation energies, disposition approvals).
- EUDR: "map the payload to the EU Information System (TRACES) DDS schema when submitting";
  submission is stubbed.
- One-up/one-down: the Commission's guidance on Articles 11, 12, 14, 17, 18, 19 and 20 of
  Regulation (EC) No 178/2002 recommends further information (such as supplier and customer
  addresses); only fields captured on CTEs are included. Confirm against the current guidance
  and, for food of animal origin, Implementing Regulation (EU) No 931/2011.

## Tests

From `backend/`: `eval "$(../scripts/testdb.sh)" && go test ./internal/food`.

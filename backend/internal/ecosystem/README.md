# ecosystem

Extension and outreach surfaces. The module owns the app/agent marketplace (listings,
installs, developer sandboxes), cold-chain sensor ingest with excursion detection (readings
are also captured as EPCIS sensor events), medical device UDI records with GUDID and EUDAMED
payload preparation, the learning hub (Markdown articles embedded from `learn/`) and the
benchmark dashboards (measured system performance and anonymized peer comparisons) and the
KPI library (formula-defined indicators over data the platform already holds, with daily series
and per-tenant goals).

KPI library (`kpi.go`, `kpi_defs.go`): 61 indicators in 11 categories (delivery 4, partner exchange 8,
compliance 6, operations 9, verification 4, inventory 4, recall and cold chain 8, traceability 5,
serialization and aggregation 5, agents and exceptions 4, security and audit 4). The 25 added to
the first 36 are: event recording delay, events recorded more than 24 h late, capture processing time
(p95), events without a location, critical-tracking-event recording delay; serial numbers allocated,
packs commissioned, packing (aggregation) events, saleable packs packed into a parent, serial numbers
left in active pools; recalls not closed, recall duration, excursion severity; partner connection
tests passed, AS2 messages delivered, SFTP files failed, time to acknowledgement; agent proposals
accepted, proposals waiting, time to decide, partner files waiting for a mapping; electronic
signatures captured, API keys unused for 90 days, API keys expiring in 30 days, active administrators.
Each has an id, name,
category, unit, direction (higher or lower is better), a description of the formula and a SQL
definition over existing tables. Four kinds: ratio (numerator and denominator per event; means
are ratios), sum, quantile (p95, median) and snapshot (value as of now; a daily history only
where it can be rebuilt from timestamps, for example open alerts). A KPI that has nothing to
measure in the window is null, not zero. On-time-in-full is measured from customer purchase
orders that state `dates.delivery_requested` and the buyer's receiving advices (`pkg/edi` types
`order` and `receiving_advice`): on time when the latest advice is dated on or before the requested
date, in full when the accepted received quantity reaches the ordered quantity for every GTIN.
Goals are per tenant (`kpi_goal`, migration `00022_kpi.sql`) and only tag a value as met or
missed; none ship by default. Peer comparison: `GET /benchmarks?kpis=library` appends the
peer-comparable KPIs (ratios and quantiles, not volumes or snapshots) to the benchmark dashboard
with the same k-anonymity threshold; without the parameter the dashboard is unchanged.

## Endpoints (`/api/v1`)

| Method | Path | Purpose |
|---|---|---|
| GET | `/learn`, `/learn/{slug}` | Learning hub articles (public) |
| GET | `/benchmarks/system` | Recorded system benchmark runs (public) |
| GET | `/marketplace` | Marketplace listings (public; with a valid token or API key `installed` reflects that tenant's installs) |
| GET | `/apps` | Marketplace listings |
| POST | `/apps` | Publish an app listing (admin); returns the stored `verified` and `createdAt` |
| POST, DELETE | `/apps/{id}/install` | Install (grants a scoped API key and, if declared, an EPCIS webhook subscription) / uninstall (admin) |
| GET | `/apps/installed` | Installed apps |
| POST | `/apps/sandbox` | Create a developer sandbox tenant with seed data |
| POST | `/sensors/readings` | Ingest sensor readings; detects temperature excursions |
| GET | `/sensors/excursions` | List excursions |
| GET, POST | `/udi/devices` | List / register UDI device records |
| POST | `/udi/devices/{id}/submit` | Prepare the GUDID / EUDAMED payload |
| GET | `/kpis` | KPI library with current values over `?days=30` (1-365), the tenant's goals and tags; filter `category` |
| GET | `/kpis/{id}/series` | Daily series (`?days`); `available: false` for snapshots without history |
| PUT, DELETE | `/kpis/{id}/goal` | Set / remove the tenant's goal (admin, ops, compliance; audited) |
| GET | `/benchmarks` | Tenant benchmark dashboard and peer comparison (`?kpis=library` adds library KPIs) |
| POST | `/benchmarks/runs` | Record a benchmark run (operator only) |

## Background work

None.

## Data

`app_listing`, `app_install`, `sensor_reading`, `sensor_excursion`, `udi_device`,
`benchmark_run` (migration `00010_ecosystem.sql`); `kpi_goal` (migration `00022_kpi.sql`). App installs also write `api_key` and
`epcis_subscription` rows.

## Depends on

`core/*`, `epcis` (captures sensor and sandbox events), `platform` (sandbox tenant
creation), `pkg/epcis`, `pkg/gs1`.

## Spec gaps

- GUDID: "accepts HL7 SPL XML through the FDA Electronic Submissions Gateway; map these
  fields to the SPL structure ... before use."
- EUDAMED: "machine-to-machine uses the Commission's XSD-defined XML; map to the current
  'UDI/Device registration' data dictionary before use."
- Submission: "transmission requires gateway onboarding (FDA ESG / EUDAMED M2M); payload
  prepared locally" — nothing is transmitted.

## Tests

From `backend/`: `eval "$(../scripts/testdb.sh)" && go test ./internal/ecosystem`. The KPI tests build
fixtures with the superuser connection and check every KPI's value, the daily series, goals,
roles, audit entries, tenant isolation and the peer benchmarks; the aggregation maths is tested
without a database.

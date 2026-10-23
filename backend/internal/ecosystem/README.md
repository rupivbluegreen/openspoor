# ecosystem

Extension and outreach surfaces. The module owns the app/agent marketplace (listings,
installs, developer sandboxes), cold-chain sensor ingest with excursion detection (readings
are also captured as EPCIS sensor events), medical device UDI records with GUDID and EUDAMED
payload preparation, the learning hub (Markdown articles embedded from `learn/`) and the
benchmark dashboards (measured system performance and anonymized peer comparisons).

## Endpoints (`/api/v1`)

| Method | Path | Purpose |
|---|---|---|
| GET | `/learn`, `/learn/{slug}` | Learning hub articles (public) |
| GET | `/benchmarks/system` | Recorded system benchmark runs (public) |
| GET | `/marketplace` | Marketplace listings (public) |
| GET | `/apps` | Marketplace listings |
| POST | `/apps` | Publish an app listing (admin) |
| POST, DELETE | `/apps/{id}/install` | Install (grants a scoped API key and, if declared, an EPCIS webhook subscription) / uninstall (admin) |
| GET | `/apps/installed` | Installed apps |
| POST | `/apps/sandbox` | Create a developer sandbox tenant with seed data |
| POST | `/sensors/readings` | Ingest sensor readings; detects temperature excursions |
| GET | `/sensors/excursions` | List excursions |
| GET, POST | `/udi/devices` | List / register UDI device records |
| POST | `/udi/devices/{id}/submit` | Prepare the GUDID / EUDAMED payload |
| GET | `/benchmarks` | Tenant benchmark dashboard and peer comparison |
| POST | `/benchmarks/runs` | Record a benchmark run (operator only) |

## Background work

None.

## Data

`app_listing`, `app_install`, `sensor_reading`, `sensor_excursion`, `udi_device`,
`benchmark_run` (migration `00010_ecosystem.sql`). App installs also write `api_key` and
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

From `backend/`: `eval "$(../scripts/testdb.sh)" && go test ./internal/ecosystem`.

# connectors

Country regulatory connectors and integration connectors. The module owns the connector catalog, per-tenant connector
instances, the mapping from captured EPCIS events to regulatory reports, submission with
retries and backoff, status polling, the regulatory alert inbox (parsed reason, affected
serials, suggested fix, resubmit) and the versioned YAML rule packs in `backend/rulepacks`
(`ae-tatmeen`, `eg-eda`, `eu-fmd`, `eu-eudr`, `sa-rsd`, `us-dscsa`, `us-fsma204`), and the SFTP
file exchange with trading partners (outbound EPCIS file drop, inbound EPCIS/EDI polling).

Connectors implement the interface from the separate `connectors-sdk` Go module.
Sub-packages:

- `eufmd` — EU FMD pack data upload and status changes (`eu-fmd-emvs`).
- `sarsd` — Saudi SFDA RSD notifications (`sa-rsd`).
- `aetatmeen` — UAE Tatmeen reports (`ae-tatmeen`).
- `egeda` — Egypt EDA reports (`eg-eda`).
- `hubjson` — shared engine of `aetatmeen` and `egeda`. Both hubs distribute their interface
  specifications only to onboarded parties, so the engine speaks one minimal, documented JSON
  shape (`POST /reports` → `{reportId}`, `GET /reports/{id}` → `{status, errorCode,
  errorMessage, failedItems}`) against the mock hubs. Every element of it is `TODO(spec)`.
- `sftpdrop` — SFTP integration connector (`sftp`): outbound EPCIS 2.0 file drop (one JSON-LD
  or XML document per capture, written as `.part` then renamed) and the session primitives for
  inbound polling. Host keys are pinned by SHA-256 fingerprint; unknown keys are refused. An instance
  bound to a partner (`partnerId`, `documentDir`, `documentFormat` x12 / edifact / auto) also takes
  outbound B2B documents: `Module.DropDocument` renders the document with `pkg/edi` and writes it
  as `<prefix>-<type>-<number>-<hash>.x12|.edi` (same content: same name, so a resend is idempotent).
  Wired into link's send by the application; logged in `sftp_file` (direction outbound) and audited as
  `sftp.outbound.processed|failed`.
- `mockhub` — in-memory mock hubs reachable at `http://<name>.mock.local` through an
  in-process transport (EU Hub, RSD, Tatmeen, EDA), and an in-process SFTP server with an
  in-memory file system for tests, for development, demos and contract tests.

## Endpoints (`/api/v1`, roles admin / compliance / integration)

| Method | Path | Purpose |
|---|---|---|
| GET | `/connectors` | Connector catalog |
| GET, POST | `/connectors/instances` | List / create tenant instances |
| PUT | `/connectors/instances/{id}` | Update an instance |
| POST | `/connectors/instances/{id}/submit` | Queue a capture for this instance manually |
| GET | `/connectors/instances/{id}/files` | SFTP inbound file log (`?status=processed\|failed`) |
| POST | `/connectors/instances/{id}/sftp/poll` | Poll the SFTP inbound folder now |
| POST | `/connectors/instances/{id}/sftp/test` | Connect with the stored settings and list the folders |
| POST | `/connectors/sftp/host-key` | Read a server's host key fingerprint (for out-of-band comparison; pins nothing) |
| GET | `/reports`, `/reports/{id}` | List / get regulatory reports |
| POST | `/reports/{id}/resubmit` | Resubmit a failed report |
| GET | `/rulepacks`, `/rulepacks/{id}` | List / get rule packs |
| POST | `/rulepacks/evaluate` | Evaluate rule packs against input |

## Background work

Jobs (on `core/jobs`):
- `connectors.dispatch` — enqueued by `epcis` on every capture; maps events to reports.
- `connectors.submit` — sends a report to the authority, with retries.
- `connectors.poll` — polls submission status.

Schedule `connectors.sftp.inbound` (every 2 min) polls every enabled `sftp` instance with an
inbound folder. Each file (at most 100 per poll, 64 MiB each) is captured through the EPCIS
module's capture path (`.json`/`.jsonld`/`.xml`, content sniffed otherwise) or, when it starts
with `ISA` / `UNA` / `UNB`, parsed with `pkg/edi` (X12 850, EDIFACT ORDERS) and stored as an
inbound B2B document. Processed files move to the archive folder; failed files move to the
error folder (or stay when none is set) and raise an `integration.sftp` alert with the reason
and a suggested fix. Connection failures (including a changed host key, naming the presented
fingerprint) raise one alert per instance, resolved by the next successful poll. A
PostgreSQL advisory lock keeps one poll per instance at a time. The application wires the
capture path (`Module.Capture`) and the B2B document store (`Module.StoreEDI`).

Instance settings are validated on save by connectors implementing `Validate(Config)`.
Secrets (`password`, `privateKey`, `passphrase`, hub client secrets) are sealed with the tenant
keyring, as for every connector. Connectors whose trigger list is `*` receive every event,
narrowed by the instance's `bizSteps` setting; connectors implementing
`connector.BatchMapper` get one call per capture.

## Data

`connector_instance`, `regulatory_report` (migration `00005_connectors.sql`), `sftp_file`
(migration `00014_sftp_files.sql`). Failures are raised as rows in the platform `alert` table.
Inbound file outcomes are written to the audit trail (`sftp.inbound.processed|failed`).

Rule packs may list rules that cannot yet be attributed to a public source with an empty
`source` and `todo: "TODO(spec): …"`; they are shown but never evaluated.

## Depends on

`core/*`, `masterdata`, `trace`, `rulepacks`, `pkg/epcis`, `pkg/edi`, `connectors-sdk`,
`golang.org/x/crypto/ssh`, `github.com/pkg/sftp`. EPCIS capture and B2B document storage are
injected by `app` (no import of `epcis` or `link`).

## Spec gaps

- `eufmd`: "the EU Hub interface specification is distributed by EMVO to onboarded partners
  and is not public" — paths and codes are the mock hub's; the base URL is a placeholder.
- `sarsd`: "RSD exposes SOAP web services documented in SFDA's integration guide" — this
  connector speaks JSON to the mock hub; spec version and service URL are open.
- `aetatmeen`, `egeda`, `hubjson`: the Tatmeen and EDA interface specifications are distributed
  to onboarded parties. Base URLs, authentication (basic credentials are a placeholder), the
  participant identifier (`X-Participant-Id` placeholder), report types, field names, error codes
  and aggregation reporting are all `TODO(spec)`. Rejection codes are the mock hubs' (`MOCK-`).
- `rulepacks/ae-tatmeen.yaml`, `rulepacks/eg-eda.yaml`: only the GS1 AI (21) serial length is
  evaluated; carrier content, party identification, master data, reportable events,
  aggregation and timelines are `TODO(spec)` until the decree texts are cited.

## Tests

From `backend/`: `eval "$(../scripts/testdb.sh)" && go test ./internal/connectors/...`.
`contract_test.go` runs against the mock hubs without a database.

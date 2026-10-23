# Open Spoor

**The Open Agentic Supply Chain OS.** Trace every pack, link every partner, act through governed agents, comply with every market.

Open Spoor is an Apache-2.0, self-hostable operating system for regulated supply chains. Pharma first (US DSCSA, EU FMD, Saudi RSD), food second (FSMA 204, EUDR). It is built only on public standards: GS1 EPCIS 2.0, CBV 2.0, Digital Link and Lightweight Messaging for VRS. *Spoor* is Dutch for track or trace.

Site: <https://rupivbluegreen.github.io/openspoor/>

## Performance

Measured on a 4 vCPU Intel Xeon @ 2.80 GHz VM, with PostgreSQL 16 on the same host (fsync on) and one Go 1.26 process. Raw results are in [`bench-results/`](bench-results).

| Benchmark | Result | Budget |
|---|---|---|
| Capture one **100,000-event** EPCIS 2.0 document (33 MB), atomically | **6.1 s** (≈16,400 events/s) | — |
| Projection of those events into serial state | ≈19,300 events/s | — |
| Capture acknowledgement p95, 1k-event document | 121 ms | < 200 ms |
| Same over HTTP (k6), 1 / 2 / 4 concurrent clients | 130 / 163 / **234** ms p95 | < 200 ms: met up to 2 clients |
| VRS verification p95 at 200 requests/s (k6) | 18.7 ms | < 1 s |
| FF1 random serial allocation through the API | 235k–530k serials/s | — |

Reproduce with `make -C backend bench` and `K6=$(command -v k6) VUS=2 scripts/loadtest.sh`.

## Quick start

```sh
git clone https://github.com/rupivbluegreen/openspoor && cd openspoor
docker compose -f deploy/docker-compose.yml --profile app up -d --build
docker compose -f deploy/docker-compose.yml --profile app run --rm openspoor seed --packs 1000
```

Open <http://localhost:8080> and sign in to tenant `nordwind` as `admin@nordwind.example` / `openspoor-demo`. The seed creates three fictional companies (Nordwind Pharma, Harbor Distribution, Tulip Pharmacy) with products, serial pools, commissioned and aggregated packs, shipments, a mock EU Hub connection, a food lot and cold-chain readings. The public verify page is at `/verify/` and the operator console at `/operator/` (`ops@openspoor.example`).

### Development

```sh
docker compose -f deploy/docker-compose.yml up -d postgres   # or scripts/testdb.sh for a throwaway instance
make -C backend migrate seed run                              # API + worker on :8080
pnpm -C frontend install && pnpm -C frontend --filter @openspoor/console dev   # console on :5173, proxies /api
```

Tests: `eval "$(scripts/testdb.sh)" && make -C backend test` (unit and integration tests against a throwaway PostgreSQL 16), `make -C backend lint arch-lint`, `pnpm -C frontend -r typecheck test build`.

## What's in it

| Pillar | Capabilities |
|---|---|
| **Trace** | Serial pools with FF1 format-preserving encryption (random, never reissued) or sequential; idempotent allocation; GS1 element strings, Digital Link and DataMatrix encode/decode; EPCIS 2.0 capture (JSON-LD, XML 2.0, XML 1.2) with per-event validation errors; query interface, named queries and HMAC-signed webhooks; aggregation trees and serial history. |
| **Link** | Partner directory, invitations and connections; DSCSA TI/TS generation, outbound exchange over HTTPS, inbound reconciliation against the ASN; VRS responder and requester; EDI mappers (X12, EDIFACT, IDoc); supplier compliance; node-to-node federation with `did:web` identities and Ed25519-signed messages. |
| **Act** | Governed agents with scoped tools, human approval gates, full audit and reversible actions; a control tower; cases; a workflow designer; an assistant over your own data (Claude, through the official Go SDK). |
| **Comply** | EU FMD (EMVS) and Saudi RSD connectors built on an open connector SDK, tested against bundled mock hubs; versioned YAML rule packs; a regulatory alert inbox with parsed rejection reasons and one-click resubmit; FSMA 204 sortable spreadsheet; EUDR due-diligence statements; UDI (GUDID and EUDAMED) payloads. |
| **Platform** | PostgreSQL row-level security per tenant; hash-chained append-only audit log; e-signatures with re-authentication (21 CFR Part 11 style); SaaS mode with signup, branding, feature flags and usage metering; GDPR export and tenant deletion by crypto-shredding; GAMP 5 validation pack. |

Regulator-facing integrations run against mocks until a deployer completes onboarding with the relevant authority. Interface details we could not verify are marked `TODO(spec)` in the code and listed in [`docs/OPEN_QUESTIONS.md`](docs/OPEN_QUESTIONS.md). Open Spoor does not invent regulatory requirements.

## Architecture

One Go binary (`openspoor api | worker | all | migrate | bootstrap | seed | openapi`) built as a modular monolith. Module boundaries are enforced by `go-arch-lint`.

- **PostgreSQL 16**: tenants isolated by forced RLS; EPCIS events month-partitioned; serial state hash-partitioned; transactional-outbox job queue (`SKIP LOCKED`).
- **Redpanda** (Kafka-compatible): the event bus is mirrored there when `OPENSPOOR_KAFKA_BROKERS` is set.
- **Keycloak** or any OIDC provider for SSO; built-in password login and API keys otherwise.
- **Frontend**: React 19, Ant Design 6 and pro-components, in English, Dutch and Arabic (RTL). Three apps (console, verify, operator) served by the binary from `OPENSPOOR_WEB_ROOT`.
- **API**: REST under `/api/v1`, OpenAPI 3.1 generated from the router ([`backend/internal/app/openapi.yaml`](backend/internal/app/openapi.yaml)), with RFC 9457 problem responses. Events (the Redpanda topics and signed subscription webhooks) are described in AsyncAPI 3.0 ([`backend/internal/app/asyncapi.yaml`](backend/internal/app/asyncapi.yaml), served at `/api/v1/asyncapi.yaml`).

Decisions are recorded in [`docs/adr/`](docs/adr).

## Deployment modes

The same image runs everywhere; `OPENSPOOR_MODE` selects `saas` (multi-tenant, host-based tenant routing), `single`, or `hub`. Kubernetes: [`deploy/helm/openspoor`](deploy/helm/openspoor). Backup and restore: [`docs/runbooks/backup-restore.md`](docs/runbooks/backup-restore.md). Air-gapped sites: `scripts/airgap-bundle.sh` builds one tarball with every container image, the compose file, the Helm chart and an installer; `install.sh` on the target host needs only Docker.

| Variable | Default | Purpose |
|---|---|---|
| `OPENSPOOR_DATABASE_URL` | local dev URL | Application role (must not be a superuser, so RLS applies) |
| `OPENSPOOR_MIGRATE_DATABASE_URL` | database URL | Role used by `migrate` |
| `OPENSPOOR_MIGRATE_ON_START` | `false` | Apply migrations before `api`, `worker` or `all` start |
| `OPENSPOOR_MODE` | `single` | `saas`, `single` or `hub` |
| `OPENSPOOR_JWT_SECRET` | — | Session signing key (at least 16 bytes) |
| `OPENSPOOR_MASTER_KEY` | — | Wraps per-tenant data keys and the federation key |
| `OPENSPOOR_OIDC_ISSUER`, `OPENSPOOR_OIDC_AUDIENCE` | — | External SSO over OIDC; SAML identity providers connect through Keycloak ([runbook](docs/runbooks/sso.md)) |
| `OPENSPOOR_PUBLIC_URL`, `OPENSPOOR_BASE_DOMAIN` | localhost | Links, `did:web` identity, SaaS host routing |
| `OPENSPOOR_WEB_ROOT` | — | Directory with the built `console`, `verify` and `operator` apps |
| `OPENSPOOR_KAFKA_BROKERS` | — | Mirror the event bus to Redpanda |
| `OPENSPOOR_BILLING`, `OPENSPOOR_LAGO_URL`, `OPENSPOOR_LAGO_API_KEY` | `none` | Usage metering |
| `OPENSPOOR_UNLEASH_URL`, `OPENSPOOR_UNLEASH_TOKEN` | — | Feature flags (OpenFeature) |
| `ANTHROPIC_API_KEY`, `OPENSPOOR_ASSISTANT_MODEL` | — | Model access for the assistant; without both it answers from the gathered data only |
| `OPENSPOOR_S3_ENDPOINT`, `OPENSPOOR_S3_BUCKET`, `OPENSPOOR_S3_ACCESS_KEY`, `OPENSPOOR_S3_SECRET_KEY` | — | Archive of old captures as gzipped EPCIS 2.0 files (SeaweedFS or any S3 service; the bucket must exist) |
| `OPENSPOOR_ARCHIVE_AFTER_DAYS` | `90` | Age at which a capture is archived; events stay queryable in PostgreSQL |
| `OPENSPOOR_SMTP_URL`, `OPENSPOOR_MAIL_FROM` | — | SMTP relay (`smtp://user:pass@host:587`) for opt-in recall notices from the public verify page; off when unset |
| `OTEL_EXPORTER_OTLP_ENDPOINT` | — | OpenTelemetry traces over OTLP/HTTP (HTTP requests, SQL queries, background jobs); off when unset |
| `OPENSPOOR_DEV_AUTH` | `false` | Development shortcuts; never in production |

## Quality and validation

- 42 backend test functions (53 with subtests), most of them integration tests against PostgreSQL 16. CI runs them with the race detector, plus golangci-lint, go-arch-lint and an OpenAPI diff on pull requests.
- OWASP ZAP baseline across all 210 documented URLs: 0 High, Medium or Low findings ([`docs/security/zap-baseline.md`](docs/security/zap-baseline.md)).
- GAMP 5 validation pack: URS, risk assessment, IQ template, and an OQ report generated from the test run and traced to every requirement ([`docs/validation/`](docs/validation)).

## Repository layout

```
backend/          Go module: cmd/, internal/ (modules), pkg/ (reusable GS1, EPCIS and EDI libraries), migrations/, rulepacks/
connectors-sdk/   Standalone Go module for third-party country connectors, with a contract-test harness
frontend/         pnpm workspace: apps/console, apps/verify, apps/operator, packages/ui
deploy/           Dockerfile, docker-compose, Helm chart, k6 scripts
docs/             ADRs, changelog, open questions, runbooks, security, validation
site/             GitHub Pages site
```

## Contributing

See [CONTRIBUTING.md](CONTRIBUTING.md) and [CODE_OF_CONDUCT.md](CODE_OF_CONDUCT.md). Report vulnerabilities as described in [SECURITY.md](SECURITY.md). Changes are recorded in [docs/CHANGELOG.md](docs/CHANGELOG.md).

## License

[Apache-2.0](LICENSE). Open Spoor implements public standards only and contains no vendor code or branding. GS1 standards are © GS1 and are referenced, not redistributed.

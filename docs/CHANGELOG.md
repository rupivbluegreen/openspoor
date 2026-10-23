# Changelog

All notable changes. Dates are UTC.

## Unreleased
- M0: Go single binary (api/worker/all/migrate/bootstrap/seed), PostgreSQL RLS tenancy, hash-chained audit log, job queue, event bus (Redpanda mirror), Docker Compose (Postgres, Redpanda, Keycloak, Temporal, SeaweedFS), CI (build, vet, race tests on Postgres 16, golangci-lint, go-arch-lint, OpenAPI diff, frontend typecheck/test/build).
- M1: organizations, sites (GLN), licenses, users and roles, API keys, audit log + chain verification, e-signatures with re-authentication, alerts inbox; products with CSV import and packaging hierarchy per market.
- M2: serial pools (FF1 random or sequential), idempotent allocation, commissioned bitmaps, pool-low alerts, GS1 element string / Digital Link / DataMatrix encode and decode.
- M3: EPCIS 2.0 capture (JSON-LD, XML 2.0 and 1.2), CBV and master-data validation with per-event errors, idempotent re-capture, query interface with pagination, named queries, HMAC-signed webhook subscriptions, serial state and aggregation projections, serial history and tree.
- M4: DSCSA TI/TS generation per shipment, outbound over HTTPS (AS2 through an interface), inbound reconciliation against the ASN, VRS responder and requester with a lookup directory; partner directory, invitations and connections.
- M5: connector SDK (separate Go module, `github.com/openspoor/connectors-sdk`) with a contract-test harness; EU FMD (EMVS) and Saudi RSD connectors against bundled mock hubs; parsed rejection reasons in the regulatory alert inbox with one-click resubmit; versioned YAML rule packs (EU FMD, US DSCSA, SA RSD, US FSMA 204, EU EUDR).
- M6: EDI mappers (X12 856/850, EDIFACT DESADV/ORDERS, IDoc) and B2B documents; supplier compliance certificates.
- M7: Helm chart; backup and restore runbook; GAMP 5 validation pack (URS, risk assessment, IQ template, OQ report generated from tests and traced to the URS); OpenAPI 3.1 generated from the router, with a staleness test; k6 load tests; OWASP ZAP baseline (0 High, Medium or Low findings); CSP and HSTS headers; the binary serves the console, verify and operator apps; migrate-on-start for compose.
- M8: SaaS mode with host-based tenant routing, self-service signup and plans, per-tenant branding, OpenFeature flags (Unleash provider), Lago usage metering, GDPR export, and tenant deletion by crypto-shredding the per-tenant data key.
- M9: food: lots, CTEs and KDEs, transformations, FSMA 204 sortable spreadsheet, EUDR plots and due-diligence statements, recalls, cold-chain sensor readings and excursions. Federation: did:web node identity, Ed25519-signed envelopes, replay protection.
- M10: governed agents with scoped tools, approval gates and reversible actions; control tower; case management; workflow designer; assistant over tenant data (Claude via the official Go SDK, with refusals handled).
- M11: app marketplace with sandboxed developer apps; learn articles; peer benchmarks with k-anonymity (k = 5); UDI device registration (GUDID and EUDAMED payloads prepared locally).
- Migration tooling: CSV and EPCIS imports from incumbent systems; shadow runs that compare final serial states.
- Performance: a 100,000-event EPCIS document is captured atomically in 6.1 s (≈16.4k events/s) on 4 vCPU. Hot queries carry an explicit tenant predicate, because the RLS `OR` operator-bypass prevented index use under generic plans. Bulk capture stages through COPY into a temporary table, since PostgreSQL forbids COPY into RLS tables.
- Frontend: console, operator and verify apps (React 19, Ant Design 6, pro-components) in English, Dutch and Arabic (RTL); EPCIS playground.
- Site: GitHub Pages site with the measured benchmarks.

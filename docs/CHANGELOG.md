# Changelog

All notable changes. Dates are UTC.

## Unreleased
- M0: Go single binary (api/worker/all/migrate/bootstrap/seed), PostgreSQL RLS tenancy, hash-chained audit log, job queue, event bus (Redpanda mirror), Docker Compose (Postgres, Redpanda, Keycloak, Temporal, SeaweedFS), CI (build, vet, race tests on Postgres 16, golangci-lint, go-arch-lint, OpenAPI diff, frontend typecheck/test/build), ADR-001…006.
- M1: organizations, sites (GLN), licenses, users and roles, API keys, audit log + chain verification, e-signatures with re-authentication, alerts inbox; products with CSV import and packaging hierarchy per market.
- M2: serial pools (FF1 random or sequential), idempotent allocation, commissioned bitmaps, pool-low alerts, GS1 element string / Digital Link / DataMatrix encode and decode.
- M3: EPCIS 2.0 capture (JSON-LD, XML 2.0 and 1.2), CBV and master-data validation with per-event errors, idempotent re-capture, query interface with pagination, named queries, HMAC-signed webhook subscriptions, serial state and aggregation projections, serial history and tree.

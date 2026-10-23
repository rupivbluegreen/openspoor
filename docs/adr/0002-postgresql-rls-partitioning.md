# ADR-002: PostgreSQL 16 with row-level security, partitioned event store, goose migrations

- Status: accepted
- Date: 2026-10-22

## Decision
- Every tenant table has `tenant_id` and a forced RLS policy `tenant_id = app_tenant() or app_operator()`. The app connects as a non-superuser role; each request runs in a transaction that sets `app.tenant_id` (`db.Tx`). Operator code paths use `db.OperatorTx` explicitly. Tests prove cross-tenant reads and writes fail, including raw SQL without a tenant set.
- `epcis_event` is range-partitioned by month on `record_time` (`ensure_epcis_partition`), with a GIN index on canonical EPCs (large pending list for bulk capture). `serial_state` is hash-partitioned by tenant (8 partitions).
- PostgreSQL forbids `COPY` into RLS tables, so bulk capture COPYs into a session temp table and then `INSERT … SELECT`s (RLS-checked) in the same transaction.
- Identifiers are stored canonically (GS1 Digital Link on id.gs1.org) so URN and Digital Link forms of the same object match.
- Migrations: goose, embedded in the binary (`openspoor migrate`).
- All SQL is Postgres-compatible and goes through Go interfaces so the serial/event state store can move to YugabyteDB (Apache 2.0) at very large scale.

## Consequences
Tenant isolation is enforced by the database, not only by application code. Bulk capture costs one extra in-memory copy; measured numbers are in `bench-results/`.

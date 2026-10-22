# Backup and restore runbook

Open Spoor keeps all state in PostgreSQL (plus optional archived documents in S3). Back up the database; everything else is reproducible from the image and configuration.

## What to protect
| Item | Where | How |
|---|---|---|
| Database | PostgreSQL 16 | continuous WAL archiving + nightly base backup (below) |
| Master key `OPENSPOOR_MASTER_KEY` | secret store | store with the same care as the backups — without it sealed tenant keys, pool keys and connector secrets cannot be opened |
| JWT secret | secret store | rotate freely (users sign in again) |
| Archived EPCIS documents (optional) | S3 bucket (SeaweedFS) | bucket versioning + replication |

## Backup (recommended: pgBackRest or WAL-G; minimal: pg_dump)
1. Enable WAL archiving (`archive_mode=on`, `archive_command` from your tool) for point-in-time recovery.
2. Nightly base backup: `pgbackrest --stanza=openspoor backup --type=full` (or `wal-g backup-push $PGDATA`).
3. Logical copy for portability (weekly): `pg_dump -Fc -d "$OPENSPOOR_DATABASE_URL" -f openspoor-$(date +%F).dump`.
4. Verify every backup: restore to a scratch instance (step "Restore") and run `GET /api/v1/audit/verify` for each tenant — the audit hash chain must be intact.
5. Retention: keep backups at least as long as the longest regulatory retention you serve (DSCSA records: 6 years).

## Restore
1. Stop writers: scale `openspoor api` and `openspoor worker` to 0.
2. Restore the base backup and replay WAL to the target time (`pgbackrest restore --type=time --target="2026-10-22 13:00:00+00"`), or `pg_restore -d <empty db> openspoor-YYYY-MM-DD.dump`.
3. Recreate the application role if needed: `create role openspoor login password '...'` (non-superuser, so row-level security applies) and `alter database openspoor owner to openspoor`.
4. Run `openspoor migrate` (no-op if the schema is current).
5. Start one API replica and check: `/readyz`, sign-in, `GET /api/v1/audit/verify` (intact), `GET /api/v1/trace/stats` (expected counts).
6. Start workers. Queued jobs (projections, webhooks, connector submissions) resume from the `job` table; regulatory submissions are idempotent per report key.
7. Record the restore in the change log with the e-signature of the responsible person.

## Disaster recovery targets (single region, defaults)
- RPO: ≤ 5 minutes with WAL archiving.
- RTO: ≤ 2 hours for databases up to 1 TB on a standard instance.

## Drills
Run a restore drill each quarter. Attach the drill output (verify chain result, counts) to the validation pack (`docs/validation`).

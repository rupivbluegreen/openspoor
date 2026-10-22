# Installation Qualification — Open Spoor

| Step | Instruction | Expected result | Result | Signature / date |
|---|---|---|---|---|
| IQ-01 | Record image digest: `docker inspect --format '{{.RepoDigests}}' ghcr.io/openspoor/openspoor:<version>` | Digest matches the release notes | | |
| IQ-02 | `openspoor version` | Expected version | | |
| IQ-03 | Confirm PostgreSQL major version: `select version()` | PostgreSQL 16.x | | |
| IQ-04 | Confirm application role is not superuser: `select rolsuper, rolbypassrls from pg_roles where rolname = current_user` | `f, f` | | |
| IQ-05 | `openspoor migrate` | "migrations applied", version = release schema version | | |
| IQ-06 | Secrets: `OPENSPOOR_MASTER_KEY`, `OPENSPOOR_JWT_SECRET` set from the secret store, `OPENSPOOR_DEV_AUTH` unset | Service starts; dev login disabled | | |
| IQ-07 | TLS on the public endpoint | Valid certificate, HTTP redirects to HTTPS | | |
| IQ-08 | `GET /readyz`, `GET /metrics` | `ready`; Prometheus metrics exposed internally only | | |
| IQ-09 | Backup configured per `docs/runbooks/backup-restore.md`; first backup restored to scratch | Audit chain intact after restore | | |
| IQ-10 | Time synchronization (NTP) on all nodes | Offset < 1 s | | |

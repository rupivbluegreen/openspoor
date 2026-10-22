---
title: Self-hosting in one afternoon
tags: [platform, operations]
---
Open Spoor is one container image. The mode is configuration: `OPENSPOOR_MODE=single` (one company), `saas` (many tenants, sign-up, billing) or `hub` (national hub / air-gapped).

1. Provision PostgreSQL 16 (the only hard dependency). Create a **non-superuser** role for the app so row-level security is enforced.
2. Set `OPENSPOOR_DATABASE_URL`, `OPENSPOOR_JWT_SECRET` and `OPENSPOOR_MASTER_KEY` (32 random bytes) from your secret store.
3. Run `openspoor migrate`, then `openspoor bootstrap --slug acme --name "Acme" --email you@acme.com --password ...`.
4. Run `openspoor api` behind TLS and `openspoor worker` (or `openspoor all` on a single node).
5. Optional: Redpanda (`OPENSPOOR_KAFKA_BROKERS`) for the event log, SeaweedFS for archives, Keycloak (`OPENSPOOR_OIDC_ISSUER`) for SSO.

Back up PostgreSQL with your usual tooling (see `docs/runbooks/backup-restore.md`). The Helm chart in `deploy/helm` packages the same steps for Kubernetes.

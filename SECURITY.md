# Security policy

Open Spoor handles regulated product and trading-partner data. Please report vulnerabilities privately.

- Use GitHub's "Report a vulnerability" (Security → Advisories) on this repository.
- Include affected version/commit, a reproduction and the impact you see.
- We acknowledge within 3 working days and aim to fix critical issues within 30 days, crediting reporters who want it.

Please do not open public issues for vulnerabilities or test against installations you do not own.

## Hardening built in
- Tenant isolation enforced by PostgreSQL row-level security (forced, non-superuser app role).
- Append-only, hash-chained audit log (`GET /api/v1/audit/verify`), e-signatures with re-authentication.
- Secrets only via environment or a vault; pool keys sealed with AES-256-GCM under `OPENSPOOR_MASTER_KEY`.
- API keys stored as SHA-256 hashes, shown once; webhook payloads signed with HMAC-SHA256.

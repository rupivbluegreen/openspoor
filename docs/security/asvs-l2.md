# OWASP ASVS 4.0.3 level 2 self-assessment

| | |
|---|---|
| Date | 2026-10-23 |
| Standard | OWASP Application Security Verification Standard 4.0.3, level 2 |
| Scope | the Open Spoor backend (`backend/`), the Helm chart and CI. The frontend is covered only where noted |
| Method | reading the cited code and running the cited tests. **This is a self-assessment by the project, not a certification, audit or penetration test.** Statuses describe what the repository demonstrates, not how any one installation is operated |

Status: **met** (implemented and backed by the cited evidence), **partly** (some of the requirement group, or by deployment choice),
**not met** (a gap; listed in `docs/OPEN_QUESTIONS.md`), **n/a** (the application has no such function). Requirement numbers are
ASVS 4.0.3 groups; a group is rated by its weakest relevant requirement unless a row says otherwise.

Reproduce the automated evidence from `backend/`:
`eval "$(../scripts/testdb.sh | grep '^export')" && go test -count=1 ./internal/core/... ./internal/link/... ./internal/platform/... ./cmd/...`

## V1 Architecture, design and threat modelling

| Group | Status | Evidence or gap |
|---|---|---|
| 1.1 Secure SDLC, threat model | partly | `docs/security/threat-model.md` (short, three surfaces), ADRs, CI gates (`.github/workflows/ci.yml`: vet, race tests, golangci-lint with gosec, go-arch-lint, govulncheck, pnpm audit). No formal secure-SDLC process or security review sign-off |
| 1.2 Authentication architecture | partly | one login path, one verifier (`internal/core/auth`); OIDC for external identity providers. TOTP second factor for local accounts, optional per user and enforceable for administrators (V2.8) |
| 1.4 Access control architecture | met | single enforcement points: `auth.Middleware`, `auth.Require`/`RequireStrict`/`RequireOperator`; tenant isolation by PostgreSQL row-level security (ADR-0002, `internal/core/db`) |
| 1.5 Input and output architecture | partly | typed decoding with unknown fields rejected (`httpx.Decode`), see V5 |
| 1.6 Cryptographic architecture | partly | `internal/core/secrets` (AES-256-GCM, per-tenant keys), `docs/runbooks/secrets.md`. Single master key with a tested re-seal command (`openspoor rotate-keys`, `docs/runbooks/secrets.md`); rotation needs a maintenance window; no KMS/HSM integration |
| 1.7 Errors, logging | partly | see V7 |
| 1.8 Data protection | partly | classification not formalised; see V8 |
| 1.9 Communications | partly | TLS 1.3 by default when the process serves TLS; otherwise a deployment choice, see V9 |
| 1.10 Malicious software | partly | see V10 |
| 1.11 Business logic | partly | see V11 |
| 1.12 Secure file upload | partly | see V12 |
| 1.13 API | partly | see V13 |
| 1.14 Configuration | partly | see V14 |

## V2 Authentication

| Group | Status | Evidence or gap |
|---|---|---|
| 2.1.1 Minimum length | partly | policy minimum is 10 characters (`internal/platform/password.go`); ASVS 4.0.3 asks for 12 |
| 2.1.2 Long passwords allowed | not met | at most 72 bytes, because bcrypt ignores the rest; ASVS asks for 64 characters minimum and up to 128 (a 72-byte limit allows 64 ASCII characters but not 64 multi-byte ones) |
| 2.1.3 / 2.1.4 No truncation, Unicode allowed | met | `TestValidatePassword` (Unicode case; longer than 72 bytes is rejected, never truncated) |
| 2.1.5 / 2.1.6 Change password | met | `POST /api/v1/auth/password` needs the current password (throttled like a sign-in) and a new one that passes the password policy and differs from the old; ends all other sessions, returns a fresh token, audits `user.password.change` (no credentials in the entry) and notifies by e-mail when a relay is configured. `TestPasswordChange` |
| 2.1.7 Breached passwords | partly | embedded deny-list of about 330 common passwords, normalisation of case and symbols, a listed word plus digits, and repeated units (`common_passwords.txt`); passwords containing the e-mail local part (4+ characters) are rejected. No check against a breach corpus |
| 2.1.8 Strength meter | not verified | frontend, not assessed |
| 2.1.9 / 2.1.10 No composition rules or rotation | met | none enforced |
| 2.2.1 Anti-automation | met | `internal/platform/loginguard.go`: per account (tenant + e-mail) 5 failures, per client address 30; lockout 30 s doubling to 15 min; counters in PostgreSQL (`login_attempt`, migration 00021) so every node shares them; tests `TestLoginLockoutAndUniformErrors`, `TestLoginBackoffDoubles`, `TestLoginAddressThrottle`, `TestLoginAddressFromTrustedProxy`. A wrong second-factor code counts as a failure and the correct password does not reset the counter until the code passes (`TestMFALockoutOnWrongCodes`). Public verify, playground and directory endpoints are rate-limited per client address (in-memory, per node), and the address honours `OPENSPOOR_TRUSTED_PROXIES` (`httpx.ClientIP`; `TestPublicLimitIsPerForwardedClient`, `TestPublicDirectoryLimitIsPerForwardedClient`) |
| 2.2.2 – 2.2.3 Weaker authenticators, notifications | partly | e-mail notice (when `OPENSPOOR_SMTP_URL` is set) after a password change or reset, when the second factor is turned off, and when an administrator resets it. No notice for sign-ins from a new device or for failed attempts; nothing is sent without a relay. Recovery by e-mail is the weaker authenticator and sits behind the same policy and throttle as sign-in |
| 2.3 Authenticator lifecycle | partly | initial admin password is chosen by the operator at bootstrap and goes through the same policy; sandbox passwords are random |
| 2.4.1 – 2.4.2 Password storage | met | bcrypt, default cost 10 (`golang.org/x/crypto/bcrypt`), per-hash salt. Cost is not configurable |
| 2.4.5 Extra keyed derivation | not met | no pepper |
| 2.5 Credential recovery | partly | `POST /api/v1/auth/password/forgot` and `/reset`: 256-bit random token, stored as SHA-256, single use, valid 30 minutes, a new request replaces earlier ones; the answer is the same 202 for unknown tenants, unknown or disabled accounts and accounts without a local password; requests are throttled per account (3, then back-off) and per address, identically for existing and non-existing accounts, using the login guard's counters; a wrong token counts against the address; success ends every session and is audited. No password hint or knowledge-based question. Gaps: the token travels only by e-mail and only works with an SMTP relay (without one nothing is created); the response time of an existing account is close to, not identical to, an unknown one (mail is sent in the background); a lost second factor is reset by an administrator or with a recovery code, and the identity check for that is the organisation's. `TestPasswordRecovery`, `TestPasswordRecoveryThrottleAndNoRelay` |
| 2.6 Lookup secrets | partly | the ten recovery codes: random, single use, shown once, stored as HMAC-SHA-256 under a key derived from the master key and bound to the user. Each has about 60 bits of entropy; ASVS 2.6.2 asks for 112 bits, or a unique random salt per code, which this does not have (the keyed hash is the compensation) |
| 2.7, 2.9 Out-of-band, cryptographic authenticators | n/a | not offered (no SMS, push or hardware-key authenticator) |
| 2.8 Multi-factor | partly | TOTP (RFC 6238, SHA-1, 6 digits, 30 s, one step of drift; `internal/core/auth/totp.go`, tested against the RFC vectors): enrol, confirm with a code, disable (password and code), recovery codes. Once enabled it is required at every local sign-in, each time step is accepted once, wrong codes are throttled; the secret is sealed with the tenant data key and never audited or logged. A tenant can require it for administrators (`PUT /api/v1/tenant/security`): an administrator without it gets a session limited to enrolment, the policy cannot be switched on by someone without a factor, and administrators cannot turn their own off while it applies. Enabling, disabling and an administrator reset (`POST /users/{id}/mfa/reset`) all end the user's sessions. `TestMFAEnrolmentLoginAndRecovery`, `TestMFAPolicyForAdministrators`. Not met: optional for other roles, TOTP only (no WebAuthn or other phishing-resistant factor), e-signatures and the tenant-deletion confirmation re-authenticate with the password alone, API keys have no second factor by design, tenant switching without a password is allowed only from a session that passed a second factor when the target account has one, and OIDC users get their second factor from the identity provider |
| 2.10 Service authentication | partly | API keys are random, stored as SHA-256, revocable, role-scoped (`internal/platform`); secrets are not in source: dev defaults exist only with `OPENSPOOR_DEV_AUTH=true` (`internal/core/config`), others come from `file:`/`vault:` references (`internal/core/secretsrc`, tests with a fake Vault) |
| Uniform failure, no enumeration | met | wrong password, unknown user, inactive user and unknown tenant return the same 401 body after one bcrypt comparison (dummy hash); lockout answers the same for existing and non-existing accounts (`TestLoginLockoutAndUniformErrors`). A suspended tenant is reported as such (403) |

## V3 Session management

Local sessions are signed JWTs (HS256) or OIDC tokens. A local token carries the user's `token_version`; the stored version is checked on every request, so a token can be revoked without a session table.

| Group | Status | Evidence or gap |
|---|---|---|
| 3.1 Fundamental | met | tokens are not placed in URLs; sent as `Authorization: Bearer` |
| 3.2 Binding, generation | partly | tokens signed with the installation secret (at least 16 bytes enforced outside dev mode); algorithm allow-list `HS256`, `RS256`, `ES256` and OIDC issuer/audience checks (`internal/core/auth/auth.go`, `oidc_test.go`). A change of roles or status by an administrator, a password or second-factor change and a sign-out raise the user's token version, so tokens issued before it stop working (`TestSessionRevocation`) |
| 3.3 Logout and timeout | partly | `POST /api/v1/auth/logout` revokes the user's tokens; so do password change and reset, second-factor changes, an administrator changing roles or status, and disabling the user, all checked on every request (one indexed read; a database error refuses the request). Gaps: revocation is per user, not per device, so a sign-out ends every session of that user; the lifetime is `OPENSPOOR_JWT_TTL` (default 12 h) with no idle timeout (ASVS 3.3.2 asks for periodic re-authentication or an idle limit); OIDC tokens are revoked at the identity provider, not here; operator support tokens are not tied to a stored session and end with their support grant |
| 3.4 Cookie-based | n/a | the server issues no cookies and, since this change, no longer reads one as a credential (the `openspoor_token` fallback was removed). `TestCookieIsNotACredential` sends a valid token in several cookie names on GET and POST and expects 401; `TestNoCacheAndNoCookies` checks that no response sets a cookie or allows credentialed cross-site requests |
| 3.5 Token-based | partly | tokens carry tenant, roles, expiry and version and can be revoked as in V3.3; API keys are the long-lived credential and can be revoked |
| 3.7 Defences against session exploits | n/a | no cookie sessions issued |

## V4 Access control

| Group | Status | Evidence or gap |
|---|---|---|
| 4.1 General | met | deny by default: every `/api/v1` route sits behind the authentication middleware and a role gate; Viewer is read-only (`auth.Require`), bulk reads use `RequireStrict`; role tests in each module (for example `internal/platform/handlers_test.go`) |
| 4.2.1 Object-level access (IDOR) | met | tenant scoping is enforced in the database (`app.tenant_id`, forced RLS, `internal/core/db`); cross-tenant tests in the module suites. Platform operators bypass by design (`RequireOperator`) |
| 4.2.2 CSRF | met | credentials are sent only in the `Authorization` or `X-API-Key` header, which a browser does not attach on its own, and no cookie is a credential (V3.4), so there is no ambient authority for a cross-site request to use. CORS is an allow-list and never sends `Access-Control-Allow-Credentials`. If cookie authentication is ever added, it needs CSRF tokens first |
| 4.3 Administrative interfaces | partly | admin and operator routes are role-gated; second factor available and enforceable for administrators (V2.8) |
| Federation peers | met | inbox accepts only approved peers whose signature and, when enforced, pinned client certificate match (`internal/link/federation.go`, `federation_tls_test.go`) |

## V5 Validation, sanitization and encoding

| Group | Status | Evidence or gap |
|---|---|---|
| 5.1 Input validation | partly | JSON bodies are decoded into typed structs, unknown fields rejected, size-capped (`httpx.Decode`, 32 MiB; inbox 64 MiB); semantic validation is per endpoint and varies |
| 5.2 Sanitization, SSRF | partly | frontend output encoding by React/Ant Design (not assessed here). SSRF protection exists only for the regulatory-source watcher (`internal/act/safefetch.go`); webhook destinations, connector endpoints and peer URLs are not filtered (gap) |
| 5.3 Output encoding, injection | partly | SQL uses pgx placeholders by convention and gosec runs in CI; no dedicated injection test suite. API output is JSON |
| 5.5 Deserialization | met | JSON and XML only; Go's `encoding/xml` does not resolve external entities; no native object deserialization |

## V6 Stored cryptography

| Group | Status | Evidence or gap |
|---|---|---|
| 6.1 Data classification | partly | per-tenant sealing of keys, credentials and the node key (`internal/core/secrets`); no written classification |
| 6.2 Algorithms | met | AES-256-GCM, HMAC-SHA-256, Ed25519, SHA-256, bcrypt, TLS 1.3 (`secrets.go`, `federation.go`, `config/tls.go`); no custom primitives |
| 6.3 Random values | met | `crypto/rand` for keys, nonces and secrets (`secrets.go`, `internal/core/ids`) |
| 6.4 Secret management | partly | `file:`/`vault:` references, fail closed (`internal/core/secretsrc`); KMS-unwrapped mounts are a deployment choice (`docs/runbooks/secrets.md`). The master key lives in process memory. Rotation is `openspoor rotate-keys` (re-wraps the tenant data keys and the node key in one transaction, verified before commit, dry run available; `TestRotateKeys`, `TestRunRotateKeys`); it needs the processes stopped. Webhook secrets are sealed with the tenant data key (`secret_sealed`); rows from before the change are sealed on first delivery or by the command (`TestWebhookSecretsAreSealedAtRest`) |

## V7 Error handling and logging

| Group | Status | Evidence or gap |
|---|---|---|
| 7.1 Log content | partly | structured logs; requests log method, path, status, request id; secrets are not logged by design (`secretsrc` errors name the setting, never the value); not enforced by a test |
| 7.2 Log processing | partly | failed logins are counted, and a lockout raises an `auth.login_failures` alert plus an audit entry without e-mail or password; individual failed attempts are not logged; federation refusals are logged as warnings |
| 7.3 Log protection | partly | audit log is append-only and hash-chained, with verification (`internal/core/audit`, `GET /api/v1/audit/verify`); application logs are the operator's to protect |
| 7.4 Error handling | met | `httpx.Fail` returns a generic problem document for unexpected errors and logs the cause server-side; `Recoverer` middleware |

## V8 Data protection

| Group | Status | Evidence or gap |
|---|---|---|
| 8.1 General | partly | authenticated API responses (including 401 and 403) and the sign-in and recovery responses carry `Cache-Control: no-store` and `Pragma: no-cache` (`TestNoCacheAndNoCookies`). Public endpoints that are not about accounts (verify, directory, OpenAPI) keep their own cache rules, and static assets are cached by design |
| 8.2 Client-side | not verified | frontend not assessed |
| 8.3 Sensitive private data | partly | audit pseudonymisation (`internal/core/audit/pseudonym.go`), tenant export and crypto-shredding deletion (`internal/saas/gdpr.go`); free-text fields stay as typed (see OPEN_QUESTIONS) |
| Encryption at rest | partly | application-level sealing of keys and credentials; database, WAL and S3 encryption are storage-layer deployment choices (`docs/runbooks/secrets.md`) |

## V9 Communications

| Group | Status | Evidence or gap |
|---|---|---|
| 9.1 Client communication security | partly | with `OPENSPOOR_TLS_CERT`/`KEY` the process serves TLS 1.3 only (1.2 only if set explicitly, with AEAD forward-secret suites) and reloads renewed certificates (`internal/core/config/tls.go`, `secrets_tls_test.go`, `cmd/openspoor/main_test.go`). Otherwise TLS ends at the proxy or ingress, which the operator must configure; HSTS is sent when TLS or `X-Forwarded-Proto: https` is seen |
| 9.2.1 – 9.2.2 Server-to-service TLS | partly | outbound HTTP clients use the Go defaults (TLS 1.2+ with certificate verification). The database URL default and the compose file use `sslmode=disable`; production must set `sslmode=verify-full` |
| 9.2.3 Mutually authenticated service channels | met | federation: outbound delivery pins the peer certificate and presents this node's client certificate; the inbox requires a client certificate matching the sending peer's fingerprint (`TestFederationMutualTLSDelivery`, `TestFederationDeliveryFailsOnPinMismatch`, `TestFederationInboxEnforcesClientCertificate`, `TestClientForPinsTheLeafCertificate`). Not applicable when a proxy terminates TLS (then the proxy must do it) |
| 9.2.4 Revocation | not met | no OCSP or CRL checking; rely on short-lived certificates and re-pinning |

## V10 Malicious code

| Group | Status | Evidence or gap |
|---|---|---|
| 10.1 Code integrity | partly | code review via pull requests, CI on every change; no signed commits required |
| 10.2 Malicious code search | partly | golangci-lint with gosec; no dedicated review for back doors or time bombs |
| 10.3 Deployed application integrity | partly | `go.sum` verified builds; `govulncheck` and `pnpm audit --prod --audit-level high` in CI. Container images and releases are not signed and no SBOM is produced |

## V11 Business logic

| Group | Status | Evidence or gap |
|---|---|---|
| 11.1 Business logic security | partly | state machines are enforced server-side (capture is idempotent, serial states, approval gates for agent actions, e-signatures re-authenticate with the password); federation replays are rejected (`TestFederationTwoNodes`). No systematic abuse-case analysis or per-tenant rate limits on authenticated calls |

## V12 Files and resources

| Group | Status | Evidence or gap |
|---|---|---|
| 12.1 Upload | partly | request size caps on decode paths; no antivirus scanning, no per-tenant quota |
| 12.3 – 12.5 File execution and storage | partly | uploaded documents are stored and served as data, never executed; the served content type comes from the upload |
| 12.6 SSRF | partly | see V5.2 |

## V13 API and web service

| Group | Status | Evidence or gap |
|---|---|---|
| 13.1 Generic | partly | OpenAPI 3.1 is generated from the router and tested for staleness (`internal/app`); per-endpoint validation |
| 13.2 RESTful | partly | method and role enforcement per route; JSON bodies required; responses use RFC 9457 problem documents. No schema-validating middleware |
| 13.3 SOAP, 13.4 GraphQL | n/a | the Saudi RSD connector is a SOAP client only |
| MCP server | partly | replays the caller's credentials against the REST routes, so roles and tenant isolation are the API's (`internal/app/mcp.go`); see the threat model |

## V14 Configuration

| Group | Status | Evidence or gap |
|---|---|---|
| 14.1 Build | partly | CI builds, vets and tests (race detector) on every change; reproducibility not verified |
| 14.2 Dependencies | partly | `govulncheck` (fails on called vulnerabilities) and `pnpm audit --prod --audit-level high` run in CI; updates are manual. Local govulncheck result: see "Dependency check" below |
| 14.3 Unintended disclosure | met | development shortcuts only with `OPENSPOOR_DEV_AUTH=true` (default false); startup fails without secrets otherwise (`config_test.go`); no server banner; errors are generic |
| 14.4 HTTP security headers | met | `securityHeaders` in `internal/app/app.go`: CSP (same-origin scripts, `frame-ancestors 'none'`), `nosniff`, `X-Frame-Options: DENY`, `Referrer-Policy`, HSTS over TLS; ZAP baseline `docs/security/zap-baseline.md` |
| 14.5 Request header validation | partly | CORS is an explicit allow-list (`OPENSPOOR_CORS_ORIGINS`; `*` can be configured); `X-Forwarded-For` is trusted only for the number of proxies configured (`OPENSPOOR_TRUSTED_PROXIES`, default 0) |

## Dependency check

`govulncheck` runs in CI. A run in the development environment is recorded below when the tool and the vulnerability database
were reachable.

- `govulncheck` (2026-10-23, development sandbox): **not run**. The vulnerability database (`vuln.go.dev`) was unreachable from the sandbox
  (HTTP 403 through the egress proxy). The tool installs and loads the module; the first run happens in CI.
- `pnpm audit --prod --audit-level high` (same date, `frontend/`): passes. One low-severity advisory is reported, in `katex`
  (GHSA-238p-pmpm-9mq7, prototype pollution; reached through `@ant-design/x` > `mermaid`; fixed in katex 0.18.2). It is below the CI
  threshold and is left to a dependency update of the console.

## Open gaps

Collected in `docs/OPEN_QUESTIONS.md`, section "Security gaps". Closed in this round: password change, e-mailed password recovery,
TOTP second factor with an administrator policy, token revocation, no cookie credential, sealed webhook secrets with a master-key
rotation command, `Cache-Control: no-store`, proxy-aware public rate limits. Still open: password limits (12 characters, 72-byte
ceiling); a second factor beyond TOTP and for every role; idle timeout and per-device sessions; SSRF protection for webhooks,
connectors and peers; per-tenant API rate limits; centralised security logging; signed images and SBOM; independent penetration test.
The new code is covered by `internal/platform/account_test.go` (platform package coverage 89.5 %, measured with the test command above).

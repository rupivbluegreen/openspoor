# Threat model note: federation, public endpoints and the MCP server

Short, by design: three surfaces that accept traffic from parties the tenant does not control. Method: list assets, who can reach the
surface, what goes wrong, what stops it, and what is left. The multi-tenant API itself is covered by tenant row-level security
(ADR-0002) and the ASVS self-assessment (`asvs-l2.md`).

## 1. Federation (`/api/v1/federation/inbox`, outbound delivery; ADR-008)

Assets: the node signing key, the integrity of captured EPCIS data and B2B documents, availability of the inbox.
Reachable by: anyone on the network (the inbox route is public).

| Threat | Mitigation | Residual risk |
|---|---|---|
| Forged message from a non-peer | sender DID and tenant must resolve to an approved peer; Ed25519 signature over the canonical envelope | a stolen peer key can sign until the peer is removed or its key replaced |
| Replay of a captured message | message id stored once per tenant and direction (409 on repeat); `createdAt` within 5 minutes | ids are kept as long as the row; unbounded growth is not managed |
| Man in the middle, impersonated server | HTTPS with chain and host verification, TLS 1.3 minimum, and the peer's pinned leaf fingerprint on every outbound connection | a peer that rotates its certificate must be re-pinned or deliveries fail (by design) |
| Channel-level impersonation of the sender | when the process serves TLS, the inbox requires a client certificate that matches the sending peer's stored fingerprint and is within its dates; peers without a pin are refused | behind a TLS-terminating proxy this must be enforced at the proxy |
| Oversized or malformed payloads | 64 MiB body cap, strict JSON decoding, only known message types | a 64 MiB message still costs memory; no per-peer rate limit |
| Malicious peer pushes bad data | a captured EPCIS document goes through the same validation as any capture and is attributed to `federation:<peer>` | an approved peer is trusted to send plausible data; approve peers deliberately |
| Server-side request forgery through a peer URL | the URL is entered by a tenant admin and, when pinned, must be https; the shared egress guard (section 4) is meant to refuse internal addresses at dial time | **federation delivery is not yet wired to the guard** (the `internal/link` package is maintained separately); until then there is no private-address filter on this path (OPEN_QUESTIONS) |
| Key theft | node key sealed with the master key (`internal/core/secrets`) | master key lives in process memory |

## 2. Public verify and playground endpoints (`/api/v1/verify/*`, `/playground/*`, partner directory)

Assets: availability, the confidentiality of non-public serial data, the quality of consumer reports.
Reachable by: the public internet, no authentication.

| Threat | Mitigation | Residual risk |
|---|---|---|
| Enumeration of serial numbers, scraping | per-address rate limit (5 requests/s, burst 30); answers are limited to the verification result and product facts | distributed scraping; the limiter is per node and in memory; behind a proxy the budget is per forwarded client address only when `OPENSPOOR_TRUSTED_PROXIES` is set correctly |
| Denial of service through expensive conversions (playground) | per-address rate limit; request size caps | a modest botnet is not stopped in-process; use the edge (CDN, WAF) |
| Abuse of the recall-alert subscription (mail bombing, address harvesting) | opt-in with unsubscribe; mail off unless SMTP is configured | no CAPTCHA or double opt-in confirmation is claimed |
| Fake or flooding consumer reports | reports take a fixed reason list, details up to 1000 characters, no personal data stored, and go to the brand owner for review; per-address rate limit | volume from many addresses |
| Injection or XXE through uploaded documents | typed parsers, Go XML decoder without external entities | parser bugs; fuzzing is not in CI |
| Information leak through error text | generic problem documents | none known |

## 3. MCP server (`/mcp`)

Assets: tenant data reachable through the assistant, the ability to create cases.
Reachable by: any client holding a valid credential for a tenant (the route sits behind the authentication middleware).

| Threat | Mitigation | Residual risk |
|---|---|---|
| Assistant reads or does more than its user may | every tool replays the caller's credentials against the REST routes, so roles, tenant isolation and validation are the API's | a user can read what the role allows, and a hosted assistant then sees it: tenants decide which assistants to connect |
| Prompt injection from data (a product name or note that instructs the model) | tools are read-only except `open_case`, which only creates a case for a person to review; nothing becomes a regulated record without a human | the model may be misled into opening spurious cases or summarising wrongly; results are advice |
| Data exfiltration through the assistant | the audit log records tool calls with the user's identity | what the assistant provider does with returned data is outside this system |
| Credential theft | use scoped API keys (Integration or Viewer role) rather than admin tokens for assistants; keys are revocable | an over-privileged key is the main risk |
| Resource exhaustion | stateless handler, same body caps as the API; the route sits behind the authenticated-API rate limit (section 5), so tool calls count against the tenant and key budget | no per-tool limit |

## 4. Outbound requests to tenant-supplied destinations (SSRF)

Assets: the internal network (database, message bus, object store, other services), cloud instance credentials, and the host's loopback services.
Reachable by: any tenant administrator or integration who can enter a URL or host: webhook subscriptions, partner endpoints (DSCSA HTTPS, AS2, health), connector instances (REST hubs, SFTP hosts), federation peers, regulatory sources.

| Threat | Mitigation | Residual risk |
|---|---|---|
| A destination that is, or resolves to, an internal address (loopback, RFC 1918, unique local, link-local, carrier-grade NAT) | `internal/core/egress` checks the numeric address of every connection attempt in the dialer's `Control` hook, after DNS resolution, for every address of a multi-record answer; the default policy refuses them | a host the operator allows on purpose (below) |
| Cloud metadata endpoints (169.254.169.254, fd00:ec2::254, 100.100.100.200, 169.254.170.2, 192.0.0.192) | refused even when private addresses are allowed; only an explicit `OPENSPOOR_EGRESS_ALLOW_CIDRS` entry opens one | an operator who allow-lists metadata accepts the exposure |
| DNS rebinding: a name resolves to a public address when validated and to an internal one when connected | names are not trusted at validation time; the check is at connection time on the address actually dialled, so a rebound answer is refused (`TestDNSRebinding`) | none known for the guarded paths |
| Redirect to an internal address, a non-HTTP scheme or a downgrade to http | redirects are limited to 5, to http(s), never from https to http, and each hop is dialled through the same guard | none known |
| Address encodings (IPv4-mapped IPv6, NAT64, 6to4, Teredo, multicast, unspecified, reserved and documentation ranges) | mapped addresses are judged as the IPv4 address they carry; the prefixes that embed an IPv4 address and the reserved ranges are refused | a translation prefix the operator runs inside its own network must be allowed explicitly |
| Proxy environment variables redirecting the connection | guarded transports do not use a proxy, so the check sees the real target | installations that must egress through a proxy need an allow-listed proxy address and a different control (a gap by design) |
| Literal internal addresses saved in configuration | webhook subscriptions are validated when saved (400); host names are checked at connection | other configuration forms rely on the connection-time check only |

Settings (all read at start-up; the guard reads the current policy on every connection):

| Setting | Default | Meaning |
|---|---|---|
| `OPENSPOOR_EGRESS_ALLOW_PRIVATE` | `false` | `true` allows loopback, private, link-local and carrier-grade NAT destinations: development and air-gapped installations whose partners live on private networks. Metadata, multicast, unspecified and reserved addresses stay refused. A warning is logged at start-up |
| `OPENSPOOR_EGRESS_ALLOW_CIDRS` | empty | comma-separated CIDR blocks or addresses that are allowed whatever their class; prefer this over the previous setting in production (for example the partner's VPN range) |

Refusals are logged (`egress refused`, address and class, never the URL) and counted in `openspoor_egress_blocked_total{reason}`.
Applies to: webhooks, connector clients (REST, SFTP, hub), DSCSA HTTPS and AS2 posts, partner probes. Excluded by design: mail (SMTP), S3, Kafka, the secret store and the operator-configured billing and model endpoints. Shared helpers for other callers: `egress.Client`, `egress.NewTransport` (adjust `TLSClientConfig` for pinning), `egress.DialContext`, `egress.ValidateURL`.

## 5. Rate limiting of the authenticated API

Assets: availability of the node for all tenants.
Reachable by: any holder of a credential (user token or API key), including a runaway integration.

| Threat | Mitigation | Residual risk |
|---|---|---|
| One tenant, or one API key, exhausts the node | token buckets per tenant and per API key, sized by the tenant's plan; 429 with `Retry-After` (seconds) and an RFC 9457 problem body; a key is charged first and a refused request does not draw on the tenant bucket | in-memory and per node: with n nodes a tenant can use up to n times its limit; a distributed attacker with many tenants is the edge's job |
| A limit that blocks the platform's own work | platform operators and system principals are exempt; background jobs do not pass the HTTP middleware | an operator token is powerful: guard it |
| Public routes | not covered here: logins and public verify, playground and directory endpoints have their own per-address limits (section 2) | |

Default limits (requests per second, burst; the key bucket is separate and applies to requests made with an API key). Unauthenticated routes are not counted.

| Plan | Tenant | API key |
|---|---|---|
| community (self-hosted) | unlimited | unlimited |
| trial | 10 / 20 | 5 / 10 |
| starter | 50 / 100 | 25 / 50 |
| growth | 200 / 400 | 100 / 200 |
| enterprise | 1000 / 2000 | 500 / 1000 |

A tenant whose plan is unknown, or cannot be read, is limited as `trial`. The plan is cached for a minute. These values are engineering defaults, not product commitments: TODO(spec) in `docs/OPEN_QUESTIONS.md`.
`OPENSPOOR_RATE_LIMIT=false` turns limiting off. `OPENSPOOR_RATE_LIMITS` overrides plans: a comma-separated list of `plan=tenantRPS/tenantBurst[/keyRPS/keyBurst]`, or `plan=unlimited`; without key values the key limit is half the tenant limit (for example `starter=100/200,trial=5/10/2/4,enterprise=unlimited`). An invalid value stops the start-up.
Metrics: `openspoor_ratelimit_decisions_total{plan,scope,decision}` (decision allowed, limited or exempt; scope tenant or apikey) and `openspoor_ratelimit_buckets`. The OpenAPI description states the 429 behaviour.

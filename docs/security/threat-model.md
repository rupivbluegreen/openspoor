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
| Server-side request forgery through a peer URL | the URL is entered by a tenant admin and, when pinned, must be https | no private-address filter (gap in OPEN_QUESTIONS) |
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
| Resource exhaustion | stateless handler, same body caps as the API | no per-tool rate limit |

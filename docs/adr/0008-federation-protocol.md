# ADR-008: Node-to-node federation (design)

- Status: accepted (design, built in M9)
- Date: 2026-10-22

## Context
Open Spoor nodes (a manufacturer's self-hosted node, a wholesaler on the SaaS, a national hub) must exchange EPCIS documents, VRS requests and partner master data without a central broker and without trusting the network.

## Decision
1. **Identity.** Each node has an Ed25519 key pair. Its identifier is a `did:web` DID (`did:web:node.example.com`) whose DID document, served at `/.well-known/did.json`, lists the public key. Organisations are bound to the node with a W3C Verifiable Credential (issuer: the organisation; subject: node DID; claims: GLNs, company prefixes). Air-gapped hubs may use `did:key`.
2. **Transport.** HTTPS with mutual TLS. Client certificates are pinned per peer (fingerprint stored on the peer record); TLS terminates at the node, not at a shared proxy.
3. **Messages.** JSON envelopes `{id, type, from, to, createdAt, payload, sig}`. `sig` is a detached Ed25519 JWS over the canonical JSON of the envelope without `sig`. Types: `epcis.capture`, `vrs.request`, `vrs.response`, `partner.hello`, `partner.masterdata`, `b2b.document`.
4. **Delivery.** `POST /api/v1/federation/inbox`. Receivers verify signature, sender DID ∈ approved peers, `to` = own DID, and reject replays (`id` seen in last 7 days, `createdAt` within ±5 minutes). Responses are signed the same way. Outbound messages use the job queue (retries with backoff) so delivery survives restarts.
5. **Peering.** Peers are added by exchanging DIDs out of band or through the partner directory; both sides approve (same flow as partner invitations).

## Consequences
No shared infrastructure is needed; every message is attributable and verifiable later (non-repudiation for regulators). Key rotation is a DID document update; old keys stay listed as `revoked` so historical signatures remain verifiable.

## Implementation status (M9)
Built in `internal/link/federation.go`:
- `did:web` identity with an Ed25519 key generated on first use and sealed with the installation master key; DID document at `/.well-known/did.json`.
- Envelopes are signed with a raw Ed25519 signature (base64url), not a JWS. The signed bytes are the envelope without `sig`, with the payload **re-encoded with sorted object keys** (numbers kept verbatim). This was needed because PostgreSQL `jsonb` reorders keys, which broke verification of stored messages.
- Inbox checks: signature against the approved peer's key, `to` equals our DID, `createdAt` within ±5 minutes (`MaxSkew`), and duplicate `id` rejected with 409 (a 409 counts as delivered for the sender).
- Outbound delivery goes through the job queue, with retries and backoff.
- Mutual TLS pinning (`federation.go`, configuration in `internal/core/config/tls.go`). The peer record's `tlsFingerprint` is the SHA-256 of the peer's leaf certificate (colons, case and a `sha256:` prefix are normalised on input; an unusable value fails closed).
  - Outbound: a peer with a fingerprint is reached through an HTTP client that keeps normal chain and host-name verification (system roots plus `OPENSPOOR_TLS_PEER_CA`), requires TLS 1.3 (1.2 only with `OPENSPOOR_TLS_MIN_VERSION=1.2`), additionally requires the leaf to match the pin on every connection (`VerifyConnection`, which also runs on resumed sessions), does not follow redirects, and presents `OPENSPOOR_TLS_CLIENT_CERT`/`OPENSPOOR_TLS_CLIENT_KEY` (re-read per handshake). The pin is looked up from the peer record at delivery time. A pin on a non-https URL is refused when the peer is added and again at delivery. DID resolution for a new peer uses the same pinned client.
  - Inbound: when the API serves TLS itself (`OPENSPOOR_TLS_CERT`/`KEY`) the server asks for, but does not require, a client certificate, so the console and integrations are unaffected. `/api/v1/federation/inbox` alone requires one (`OPENSPOOR_FEDERATION_REQUIRE_CLIENT_CERT`, on by default with server TLS): no certificate gives 401; after the envelope's `from` DID and tenant resolve to an approved peer, the leaf must be within its validity dates and match that peer's stored fingerprint, otherwise 403. A peer without a stored fingerprint is refused. With `OPENSPOOR_TLS_CLIENT_CA` the handshake also verifies the chain of a presented certificate; without it the pin is the only trust anchor, which suits self-signed peers.
  - The envelope signature is still checked after this; mTLS authenticates the channel and the signature authenticates the message.
  - Tests: `internal/link/federation_tls_test.go` (generated certificates; pin match, mismatch, normalisation, missing, unpinned and expired client certificates, TLS 1.2 peers, redirects).

Behind a proxy that terminates TLS, the process cannot see a peer's certificate. Leave `OPENSPOOR_FEDERATION_REQUIRE_CLIENT_CERT` off there and enforce mutual TLS at the proxy, or terminate TLS in the pod (`docs/runbooks/secrets.md`).

Not built yet (see OPEN_QUESTIONS):
- The W3C Verifiable Credential that binds an organisation to a node.
- `did:key` for air-gapped hubs.

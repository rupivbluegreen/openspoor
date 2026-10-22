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

Not built yet (see OPEN_QUESTIONS):
- Mutual TLS pinning. The peer's TLS fingerprint is stored but not yet enforced in-process; terminate mTLS at the ingress until then.
- The W3C Verifiable Credential that binds an organisation to a node.
- `did:key` for air-gapped hubs.

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

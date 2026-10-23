# ADR-007: AS2 behind a transport interface

- Status: accepted (amended 2026-10-23: the built-in transport is now implemented)
- Date: 2026-10-22

## Context
DSCSA trading partners exchange TI/TS over AS2 (RFC 4130: S/MIME signed and encrypted messages over HTTP with signed MDN receipts) or over HTTPS APIs. We found no maintained, audited Go AS2 library. A hand-rolled S/MIME stack would be security-critical code, so the first decision kept AS2 outside the product: the built-in `as2` transport refused, and installs plugged in a gateway.

## Decision
- `dscsa.Transport` stays the boundary: `Send(ctx, from, partner, shipment, doc)`. Implementations: `https` (EPCIS capture endpoint), `local` (partner tenant on the same node) and `as2`.
- The `as2` transport is now built in. It does not implement CMS itself: signing, encryption, verification and decryption use `github.com/smallstep/pkcs7` (MIT). What we wrote is MIME framing, MDN handling and policy, in `pkg/as2` (no database, no HTTP server) and `internal/dscsa/as2.go`.
- One fixed profile: SHA-256 signatures with RSA keys, AES-256-CBC content encryption, AS2-Version 1.0, signed receipts. The library's SHA-1 and DES defaults are replaced at start. Everything else about the profile is `TODO(spec)` (see `docs/OPEN_QUESTIONS.md`), not guessed.
- Trust is pinned certificates, not a PKI: each partner connection stores the partner's certificate (sealed with the tenant key, `partner_as2`), and a signature verifies only when the signer is byte-identical to it. The node's own key is sealed with the tenant key (`as2_station`).
- Receipts: synchronous (returned in the HTTP response) and asynchronous (queued on the job queue and posted to the URL configured for the connection, never to a URL taken from the message). A receipt is verified for signature, original Message-ID, sender and message integrity check; a sender treats anything else as a failed delivery.
- Idempotency: an authenticated message is claimed by `(tenant, sender, Message-ID)` before it is processed; a repeat gets the stored receipt and is not processed again. A rejected message does not claim its Message-ID.
- Every send, receive, rejection and receipt is an audit entry and a row in `as2_message`, which the message log reads.
- The gateway option stays open: a deployment can still replace the `as2` entry of `Module.Transports` with an adapter, and inbound messages unwrapped by a gateway can still be posted to `POST /api/v1/shipments/inbound`.

## Consequences
AS2, HTTPS and same-node exchange work out of the box and are tested end to end, including a loopback pair of nodes and forged-message cases. Partner interoperability (algorithm sets, AS2 1.1 features, payload shape) has not been tested against any third-party AS2 product, and no certificate chain or revocation checking is done; both are tracked in `docs/OPEN_QUESTIONS.md`. The CMS code is a third-party dependency that must be kept up to date.

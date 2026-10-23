# ADR-007: AS2 behind a transport interface

- Status: accepted
- Date: 2026-10-22

## Context
DSCSA trading partners exchange TI/TS over AS2 (RFC 4130: S/MIME signed and encrypted messages over HTTP with signed MDN receipts) or over HTTPS APIs. Maintained, audited Go AS2 libraries do not exist today; a hand-rolled S/MIME + MDN stack would be security-critical code without a reference implementation to test against.

## Decision
- `dscsa.Transport` is the boundary: `Send(ctx, from, partner, shipment, doc)`. Implementations: `https` (EPCIS capture endpoint), `local` (partner tenant on the same node) and `as2`.
- The built-in `as2` transport returns a clear "not configured" error. Production installs that need AS2 plug in an AS2 gateway (for example an existing B2B gateway) that implements the same interface, or post to the gateway's HTTP inbound.
- Inbound AS2 arrives the same way: the gateway unwraps S/MIME and calls `POST /api/v1/shipments/inbound`.

## Consequences
HTTPS and same-node exchange work out of the box and are tested end to end; AS2 needs an adapter. Revisit when a maintained Go AS2 implementation with MDN support exists.

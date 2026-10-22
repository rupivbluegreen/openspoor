---
title: DSCSA interoperability checklist
tags: [comply, dscsa, us]
---
Since the enhanced drug distribution security requirements of FD&C Act section 582(g), US trading partners exchange **package-level** Transaction Information and Transaction Statements electronically and verify product at the package level.

1. **Product identifier on every package** — GTIN (built from the NDC), serial number (up to 20 alphanumeric characters), lot and expiry in a 2D DataMatrix.
2. **TI/TS with every change of ownership** — Open Spoor builds it as an EPCIS document from your aggregation hierarchy (`POST /shipments/{id}/tits`).
3. **Receive and reconcile** — inbound TI/TS is captured and compared with the ASN; mismatches become alerts.
4. **Verification** — answer VRS requests for your products and ask manufacturers to verify saleable returns (`/vrs`).
5. **Suspect and illegitimate product** — quarantine, investigate, notify; scans of unknown serials raise alerts automatically.
6. **Keep records for six years** — the default retention policy, which cannot be lowered.

Open Spoor implements public standards only (GS1 EPCIS/CBV, Lightweight Messaging for VRS). Where a detail is not public it is marked `TODO(spec)` in the code instead of being guessed.

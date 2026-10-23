# ADR-010: Authorized trading partner credentials as W3C Verifiable Credentials

- Status: accepted (built)
- Date: 2026-10-23

## Context
Trading partners need to show each other, and verify, that they hold a valid license before
they transact (the "authorized trading partner" check). Each Open Spoor node already has an
Ed25519 identity published as a `did:web` DID document (ADR-008). We want a credential that a
partner can present to anyone, that is verifiable without a central service, and that the
issuing node can revoke.

The W3C offers two securing mechanisms for Verifiable Credentials Data Model 2.0: embedded
Data Integrity proofs (for example the `eddsa-jcs-2022` cryptosuite) and enveloping JOSE/COSE
(VC-JOSE-COSE, a compact JWS whose payload is the credential).

## Decision
1. **Data model.** VC Data Model 2.0 JSON with `@context: ["https://www.w3.org/ns/credentials/v2"]`,
   `type: ["VerifiableCredential", "AuthorizedTradingPartnerCredential"]`, `issuer` = the node DID,
   `validFrom`, and `validUntil` = the end of the license's last valid day. `credentialSubject`
   names the organisation (and GS1 company prefix and GLN when recorded) and its license
   (type, number, issuing authority, jurisdiction, validity). No custom JSON-LD context is
   published; the extra terms fall under the issuer-dependent `@vocab` of the v2 context.
   Subjects have no DID of their own yet, so `credentialSubject.id` is omitted.
2. **Securing mechanism: Data Integrity, `eddsa-jcs-2022`** (W3C "Data Integrity EdDSA
   Cryptosuites v1.0"). We sign with an embedded `DataIntegrityProof`:
   `proofConfig` = the proof options (type, cryptosuite, created, verificationMethod,
   proofPurpose `assertionMethod`) with the document's `@context`; the signed bytes are
   `SHA-256(JCS(proofConfig)) || SHA-256(JCS(document without proof))`, signed with Ed25519, and
   `proofValue` is the base58btc multibase encoding (`z…`). JCS is RFC 8785, implemented in
   `internal/link/jcs.go` (UTF-16 key order, ECMAScript number formatting).
   We chose it over VC-JOSE because the credential stays a plain JSON document (storable in
   `jsonb`, readable by people and partners), the signature survives key reordering (which bit
   us with federation envelopes, ADR-008), and it needs no JOSE library.
3. **Key.** The node key is published twice in the DID document: `#key-1` (JsonWebKey2020, used by
   federation envelopes) and `#key-2` (Multikey, `publicKeyMultibase` = `z` + base58btc of
   `0xed01 || key`). Both are listed under `assertionMethod`; proofs reference `#key-2`.
4. **Revocation: W3C Bitstring Status List v1.0.** Each credential gets a random index in one
   node-wide list of 131,072 entries (the specification's minimum, for group privacy) and a
   `BitstringStatusListEntry` with `statusPurpose: revocation`. The node serves the list at the
   public `GET /api/v1/credentials/status/revocation` as a signed
   `BitstringStatusListCredential` (`encodedList` = `u` + base64url of the GZIP-compressed
   bitstring, index 0 being the left-most bit), rebuilt from the revoked credentials on request.
5. **Verification** (`POST /api/v1/credentials/verify`) runs and stores these checks: format
   (VC 2.0 context and type), type (`AuthorizedTradingPartnerCredential`), proof (resolve the
   issuer's `did:web` document over HTTPS, require the verification method to belong to the
   issuer and be an assertion method, verify the signature), validity window, and status (fetch
   the list, verify its proof and issuer, read the bit). Only `did:web` issuers are accepted.
   The status list must be hosted on the issuer's own host, so a credential cannot make the
   verifier fetch arbitrary URLs beyond the issuer's domain.

## Consequences
- Any party that can fetch the issuer's DID document can verify a credential; revocation is
  visible within the list's cache time (60 s).
- Credentials are bound to the node key. Rotating the key (ADR-008) must keep the old key in
  the DID document until the credentials it signed have expired.
- Issuance only attests what the tenant recorded in its license register. Whether a license
  type makes a party "authorized" under DSCSA is not decided here (see OPEN_QUESTIONS).
- `did:key` and other DID methods are not supported for issuers.

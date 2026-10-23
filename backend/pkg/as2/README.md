# as2

AS2 (RFC 4130) message layer in process: S/MIME signing and encryption, parsing and verification
of received messages, and MDN receipts (RFC 3798) with signed receipts. No database and no
HTTP server: `Build` / `Open` / `BuildMDN` / `ParseMDN` work on headers and bodies, `Post` is a
thin client. `internal/dscsa` adds identity storage, idempotency, audit and the message log.

CMS (SignedData, EnvelopedData) is `github.com/smallstep/pkcs7` (MIT); this package is MIME
framing, receipt handling and policy. The library's SHA-1 and DES defaults are replaced in
`init`.

## Profile

Defaults, with per-partner choices (configured in `internal/partners`, see below):

| Aspect | Default | Choices |
|---|---|---|
| Signature | detached CMS, SHA-256, RSA (at least 2048 bits), `micalg=sha-256` | SHA-384, SHA-512; SHA-1 only for a partner flagged legacy (`Outgoing.Legacy`, `Policy.Legacy`) |
| Encryption | AES-256-CBC content encryption, RSA key transport, enveloped around the signed entity | AES-128-CBC |
| Compression | off | CMS CompressedData (zlib), RFC 5402 style, applied to the MIME entity before signing and encryption; needs signing or encryption |
| Version | `AS2-Version: 1.0` | |
| Receipts | `multipart/report` with `message/disposition-notification`, wrapped in `multipart/signed`; sync or async; never encrypted | receipt signature digest (`MDNInput.Digest`) |
| Integrity check | digest of the signed first part (headers and body), base64, returned as `Received-Content-MIC: <mic>, <alg>`; the digest is the message's `micalg`, or the one requested in `signed-receipt-micalg` for an unsigned message | |
| Trust | pinned partner certificate: the signer must be byte-identical to it, or to the partner's second certificate during a rotation (`Peer.Other`), and valid now | |

Receivers enforce a floor: `Policy.MinDigest` (default SHA-256; SHA-1 counts only with `Policy.Legacy`) for signatures,
`Policy.MinEncryption` (default accepts AES-256 only, `aes-128-cbc` accepts both; other ciphers such as DES are refused with
`insufficient-message-security`) and `Policy.AllowCompressed`. During a certificate rotation `Station.Other` / `OtherKey` is also
tried for decryption.

Not supported (all `TODO(spec)`, see `docs/OPEN_QUESTIONS.md`): 3DES/RC2, AES-GCM, RSA-OAEP, ECC keys, AS2 1.2 filename
preservation, certificate chains and revocation. The algorithm choices and the compression order follow secondary sources and are
not verified against a partner.

## API

- `Build(station, peer, Outgoing) -> *Wire`: sign and/or encrypt a payload; headers include
  `AS2-From`, `AS2-To`, `Message-ID`, `Disposition-Notification-*` and, for async receipts,
  `Receipt-Delivery-Option`. `Wire.MIC` is the check the receipt must echo.
- `ParseHeader(h)`: identifiers and receipt preferences, enough to look up the sender.
- `Open(h, body, station, peer, Policy, now) -> *Received`: decrypt and verify. Every rejection is a
  `*Failure` whose `Code` is the MDN disposition modifier (`authentication-failed`,
  `integrity-check-failed`, `decryption-failed`, `insufficient-message-security`,
  `unexpected-processing-error`). `Policy` demands a signature and/or encryption.
- `BuildMDN`, `ParseMDN(h, body, peer, requireSigned, now)` (SHA-256 floor), `ParseMDNMin(..., minDigest, legacy)`, `IsMDN`.
- `ParseCertPEM`, `ParseKeyPEM`, `EncodeKeyPEM`, `CertPEM`, `GenerateStation`, `Fingerprint`,
  `ValidID`, `NewMessageID`, `Post`.
- Certificate lifecycle: `Describe` (subject, issuer, serial, validity, fingerprint), `ExpiryStage` (ok, 60d, 30d, 7d, expired; `ExpiryThresholds`), `CheckRotation` (a next certificate must differ from the current one, be unexpired and valid on the switch date).
- Algorithms: `NormalizeDigest`, `DigestToken`, `ValidDigest`, `ValidEncryption` and the `Digest*` / `Enc*` constants.

Identifiers (AS2 ids and Message-IDs) are restricted to `[A-Za-z0-9._:@-]`, 1 to 128 characters, so
they can sit unquoted in headers; a partner whose id needs spaces or quotes is not supported.

## Tests

`go test ./pkg/as2` (no database): all four sign/encrypt combinations, every digest and cipher (floors, SHA-1 refusal without the legacy flag, DES refusal, unknown micalg), compression (round trip, not enabled, bare, garbage, decompression bomb), certificate rotation (second signer, second decryption key, expiry), expiry stages, tampered content, a
different signer with the right AS2 id, wrong decryption key, expired certificate, policy
violations, malformed MIME, and receipts (processed, error, unsigned, forged, wrong message).

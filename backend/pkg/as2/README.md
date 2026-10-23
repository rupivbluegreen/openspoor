# as2

AS2 (RFC 4130) message layer in process: S/MIME signing and encryption, parsing and verification
of received messages, and MDN receipts (RFC 3798) with signed receipts. No database and no
HTTP server: `Build` / `Open` / `BuildMDN` / `ParseMDN` work on headers and bodies, `Post` is a
thin client. `internal/dscsa` adds identity storage, idempotency, audit and the message log.

CMS (SignedData, EnvelopedData) is `github.com/smallstep/pkcs7` (MIT); this package is MIME
framing, receipt handling and policy. The library's SHA-1 and DES defaults are replaced in
`init`.

## Profile (fixed)

| Aspect | Choice |
|---|---|
| Signature | detached CMS, SHA-256, RSA (at least 2048 bits), `micalg=sha-256` |
| Encryption | AES-256-CBC content encryption, RSA key transport, enveloped around the signed entity |
| Version | `AS2-Version: 1.0` |
| Receipts | `multipart/report` with `message/disposition-notification`, wrapped in `multipart/signed`; sync or async; never encrypted |
| Integrity check | SHA-256 over the signed first part (headers and body), base64, returned as `Received-Content-MIC` |
| Trust | pinned partner certificate: the signer must be byte-identical to it, and valid now |

Not supported (all `TODO(spec)`, see `docs/OPEN_QUESTIONS.md`): SHA-1, 3DES/RC2, AES-GCM, RSA-OAEP,
ECC keys, AS2 1.1 / 1.2 compression and filename preservation, certificate chains and revocation.

## API

- `Build(station, peer, Outgoing) -> *Wire`: sign and/or encrypt a payload; headers include
  `AS2-From`, `AS2-To`, `Message-ID`, `Disposition-Notification-*` and, for async receipts,
  `Receipt-Delivery-Option`. `Wire.MIC` is the check the receipt must echo.
- `ParseHeader(h)`: identifiers and receipt preferences, enough to look up the sender.
- `Open(h, body, station, peer, Policy, now) -> *Received`: decrypt and verify. Every rejection is a
  `*Failure` whose `Code` is the MDN disposition modifier (`authentication-failed`,
  `integrity-check-failed`, `decryption-failed`, `insufficient-message-security`,
  `unexpected-processing-error`). `Policy` demands a signature and/or encryption.
- `BuildMDN`, `ParseMDN(h, body, peer, requireSigned, now)`, `IsMDN`.
- `ParseCertPEM`, `ParseKeyPEM`, `EncodeKeyPEM`, `CertPEM`, `GenerateStation`, `Fingerprint`,
  `ValidID`, `NewMessageID`, `Post`.

Identifiers (AS2 ids and Message-IDs) are restricted to `[A-Za-z0-9._:@-]`, 1 to 128 characters, so
they can sit unquoted in headers; a partner whose id needs spaces or quotes is not supported.

## Tests

`go test ./pkg/as2` (no database): all four sign/encrypt combinations, tampered content, a
different signer with the right AS2 id, wrong decryption key, expired certificate, policy
violations, malformed MIME, and receipts (processed, error, unsigned, forged, wrong message).

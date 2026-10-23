# pkg/appsdk

Helpers for third parties building Open Spoor apps and agents. Standard library only.

| API | Purpose |
|---|---|
| `VerifyWebhook(secret, body, header)` | Check the `X-OpenSpoor-Signature` header (`sha256=<hex>` HMAC) of an EPCIS subscription delivery, in constant time |
| `WebhookHandler(secret, fn)` | `http.Handler` that verifies the signature and passes the document to `fn` |
| `New(baseURL, apiKey)` | API client for `/api/v1`; sends the app key as `X-API-Key` |
| `Client.Do` | Generic request; non-2xx responses become `*APIError` |
| `Client.Capture` | Submit an EPCIS 2.0 JSON-LD or XML document |
| `Client.Allocate` | Allocate serials from a pool with an `Idempotency-Key` |
| `Client.Events` | Run an EPCIS query and return the raw query document |
| `Manifest` | Marketplace description of an app (`POST /api/v1/apps`): id, kind, roles, optional webhook |

App installs (see `internal/ecosystem`) grant an API key with the roles the manifest requests
and, if a webhook is declared, an EPCIS subscription whose deliveries this package verifies.

## Spec gaps

None marked `TODO(spec)`.

## Tests

From `backend/`: `go test ./pkg/appsdk` (no database).

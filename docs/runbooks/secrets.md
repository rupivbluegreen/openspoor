# Secrets, TLS and key management runbook

How Open Spoor receives its secrets, how to rotate them, and where TLS ends. Nothing here is a built-in KMS
integration: envelope encryption with a cloud KMS or Vault transit is a deployment choice, described under
"KMS and Vault transit".

## Secret references

These settings accept a **reference** instead of the value. They are resolved once at startup
(`internal/core/secretsrc`, called from `internal/core/config`).

| Setting | Notes |
|---|---|
| `OPENSPOOR_MASTER_KEY` | wraps per-tenant data keys and the federation key |
| `OPENSPOOR_JWT_SECRET` | signs local session tokens |
| `OPENSPOOR_DATABASE_URL` | the whole URL, or only the password (below) |
| `OPENSPOOR_MIGRATE_DATABASE_URL` | role used by `openspoor migrate` |
| `OPENSPOOR_LAGO_API_KEY`, `ANTHROPIC_API_KEY`, `OPENSPOOR_SMTP_URL`, `OPENSPOOR_S3_SECRET_KEY` | optional integrations |

| Form | Meaning |
|---|---|
| `some-value` | used as is (environment variable; development, or a Kubernetes Secret injected as env) |
| `file:/run/secrets/master_key` | contents of the file, surrounding whitespace trimmed (Kubernetes Secret volume, Docker secret, a file written by an agent) |
| `vault:secret/openspoor/app#master_key` | field `master_key` of the KV v2 secret at mount `secret`, path `openspoor/app` (read from `/v1/secret/data/openspoor/app`) |

`OPENSPOOR_DATABASE_PASSWORD` (same forms) sets only the password of the user in `OPENSPOOR_DATABASE_URL`, so the URL can stay
in plain configuration while the password comes from a file or Vault.

Behaviour: an unreadable file, an unreachable or unauthorised Vault, a missing path or field, or an empty value stops the
process at startup with an error that names the setting and the reference, never the value. There is no fallback to an
empty secret. Values are never logged. A literal secret that itself starts with `file:` or `vault:` cannot be given as plain
text; put it in a file instead.

### Vault settings

| Variable | Purpose |
|---|---|
| `OPENSPOOR_VAULT_ADDR` | base URL; must be `https` (plain `http` only for a loopback address) |
| `OPENSPOOR_VAULT_TOKEN` | static token; wins over Kubernetes login. Prefer short-lived tokens injected by an agent |
| `OPENSPOOR_VAULT_ROLE` | role for the Kubernetes auth method: the pod's service-account token (`/var/run/secrets/kubernetes.io/serviceaccount/token`) is exchanged for a Vault token |
| `OPENSPOOR_VAULT_AUTH_MOUNT` | Kubernetes auth mount, default `kubernetes` |
| `OPENSPOOR_VAULT_NAMESPACE` | Vault Enterprise namespace header |
| `OPENSPOOR_VAULT_CACERT` | PEM file with the CA that signed Vault's certificate |
| `OPENSPOOR_VAULT_K8S_TOKEN_FILE` | override the service-account token path |

Only KV version 2 is read. The Vault policy needs `read` on `<mount>/data/<path>` and nothing else. Redirects are not followed
(a redirect would carry the token to another host).

### Helm

Put references in the Secret named by `existingSecret`, for example
`OPENSPOOR_MASTER_KEY=vault:secret/openspoor/app#master_key`, and set `vault.addr` and `vault.role` in the values. Or mount a
Secret / CSI volume and use `file:` references with `extraEnv`.

## What each secret protects, and rotation

| Secret | If lost | If leaked | Rotation |
|---|---|---|---|
| Master key | sealed tenant keys, pool keys, connector secrets and the federation key cannot be opened: restore from backup together with the database | all of the above can be unsealed with database access | not automated. Re-sealing needs a maintenance window: stop writers, unseal and re-seal `tenant_key`, `federation_node` and connector secrets with a new key. Plan it as a migration; test on a restore |
| JWT secret | nothing (users sign in again) | tokens can be forged until rotated | rotate any time; all sessions end |
| Database password | no access | data access within the role's RLS limits | change in PostgreSQL, update the reference, restart |
| API keys | n/a | revoke the key (`DELETE /api/v1/api-keys/{id}`) | issue a new key |

Never put these in values files, images, the job queue, or logs.

## KMS and Vault transit

Open Spoor does not call a cloud KMS or Vault transit itself. Two ways to keep the master key out of reach of anyone who can
read the Secret object, both outside this binary:

1. **KMS-unwrapped file mount.** Store the master key encrypted under your KMS (or use the cloud provider's secret store with
   KMS-backed encryption). An init container or CSI driver decrypts it into a memory-backed volume
   (`emptyDir.medium: Memory`) and the pod uses `OPENSPOOR_MASTER_KEY=file:/run/keys/master_key`. KMS access is granted to the
   init container's identity only.
2. **Vault.** Keep the key in KV v2 and use a `vault:` reference. Vault's own seal can be auto-unsealed by a KMS. Vault transit
   (encrypt/decrypt as a service) is not used by this binary; the master key itself is the data-encryption root.

The master key is 32 random bytes; generate it with `openssl rand -base64 32`. Keep two copies in separate custody, and test
restoring from backup with the key from custody.

## Encryption at rest

- Application level: per-tenant data keys (AES-256-GCM) seal FF1 pool keys, connector credentials, VRS partner API keys and (under the master key) the federation
  key. Webhook signing secrets are stored in the database as is; protect them with database and backup encryption; deleting a tenant destroys its key (crypto-shredding). Audit entries are pseudonymised before they are stored.
- Database and backups: use encrypted volumes or storage-level encryption for PostgreSQL, WAL archives and the S3 bucket; the
  application does not encrypt whole tables. State this in the installation record.

## TLS

**Normally a reverse proxy or ingress terminates TLS.** The container then listens on plain HTTP inside the cluster
network, `Strict-Transport-Security` is sent when `X-Forwarded-Proto: https` is present, and nothing below is needed. Use TLS 1.3
at the ingress (`ssl-protocols TLSv1.3` for ingress-nginx), and keep the internal hop on a trusted network or a service mesh.

**Serving TLS from the process** is for federation mutual TLS and for single-node installs without a proxy:

| Variable | Default | Purpose |
|---|---|---|
| `OPENSPOOR_TLS_CERT`, `OPENSPOOR_TLS_KEY` | – | PEM chain and key; when set the API serves HTTPS. Reloaded when the files change (cert-manager renewals need no restart) |
| `OPENSPOOR_TLS_MIN_VERSION` | `1.3` | `1.2` is accepted only when set explicitly; then only forward-secret AEAD suites are offered |
| `OPENSPOOR_TLS_CLIENT_CA` | – | CA for peer client certificates. Without it, a presented client certificate is not chain-verified and the pinned fingerprint is the only trust anchor |
| `OPENSPOOR_FEDERATION_REQUIRE_CLIENT_CERT` | `true` with `OPENSPOOR_TLS_CERT`, otherwise off | `/api/v1/federation/inbox` demands a client certificate whose SHA-256 fingerprint equals the sending peer's `tlsFingerprint`. The rest of the API never asks for one. Setting `true` without TLS is a startup error |
| `OPENSPOOR_TLS_CLIENT_CERT`, `OPENSPOOR_TLS_CLIENT_KEY` | – | certificate this node presents when delivering to peers (the worker needs them: it sends) |
| `OPENSPOOR_TLS_PEER_CA` | – | extra CA roots for peers whose server certificate comes from a private CA |
| `OPENSPOOR_TLS_REDIRECT_ADDR` | off | optional plain-HTTP listener (for example `:8081`) that redirects to `OPENSPOOR_PUBLIC_URL` (must be `https`) |
| `OPENSPOOR_TRUSTED_PROXIES` | `0` | number of proxies that append to `X-Forwarded-For`; used for per-address login throttling |

Federation pinning in short: a peer record has `tlsFingerprint` (SHA-256 of the peer's leaf certificate; colons, case and a
`sha256:` prefix are accepted and normalised). Outbound, the node still verifies the chain and host name (system roots plus
`OPENSPOOR_TLS_PEER_CA`), requires TLS 1.3, **and** requires the leaf to match the pin; a mismatch fails every delivery. Inbound, the
sending peer's client certificate must match its stored pin and be within its validity dates. Self-signed peers: add their
certificate to `OPENSPOOR_TLS_PEER_CA` for outbound; for inbound the pin alone is enough. Get a fingerprint with
`openssl x509 -in cert.pem -noout -fingerprint -sha256`.

When a proxy terminates TLS in front of an Open Spoor node, in-process pinning cannot see the peer's certificate: enforce
mutual TLS at the proxy for `/api/v1/federation/inbox`, or terminate TLS in the pod (Helm `tls.enabled`).

### Helm values

```yaml
tls:
  enabled: true              # pod terminates TLS: mounts Secret openspoor-tls (tls.crt, tls.key[, ca.crt])
  secretName: openspoor-tls
  minVersion: "1.3"
  clientCA: true             # verify presented client certificates against ca.crt
federationClient:
  enabled: true              # worker presents this node's client certificate to peers
  secretName: openspoor-federation-client
  peerCA: true
vault:
  addr: https://vault.example.com:8200
  role: openspoor
ingress:
  annotations:
    nginx.ingress.kubernetes.io/backend-protocol: "HTTPS"
```

With `tls.enabled` the ingress must speak HTTPS to the pod and must pass the client certificate through (TLS passthrough for the
federation host), otherwise the inbox sees no certificate and refuses the message. That is the intended fail-closed behaviour.

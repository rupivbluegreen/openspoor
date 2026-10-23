# Single sign-on (OIDC and SAML)

Open Spoor trusts one OpenID Connect provider per installation. Keycloak is the reference
provider (`deploy/keycloak/openspoor-realm.json`). SAML 2.0 identity providers (Entra ID, ADFS,
Okta, Shibboleth and others) connect through Keycloak's identity brokering, so Open Spoor itself
only ever validates OIDC access tokens.

## Configure Open Spoor

| Variable | Value |
|---|---|
| `OPENSPOOR_OIDC_ISSUER` | The realm URL exactly as it appears in the token's `iss`, e.g. `https://sso.example.com/realms/openspoor` |
| `OPENSPOOR_OIDC_AUDIENCE` | `openspoor` (default) |

Open Spoor fetches signing keys from `<issuer>/protocol/openid-connect/certs` and accepts an
access token only when all of these hold:

- signed with RS256 or ES256 by a key in that set, and not expired;
- `iss` equals `OPENSPOOR_OIDC_ISSUER`;
- `aud` contains `OPENSPOOR_OIDC_AUDIENCE`;
- `tid` is the user's Open Spoor tenant id (a UUID);
- `roles` lists the user's roles: `Admin`, `Serialization`, `Compliance`, `Ops`, `Integration` or `Viewer`.

The bundled realm already maps these: the `tenant_id` user attribute to `tid`, realm roles to
`roles`, and an audience mapper that adds `openspoor`. Keycloak puts its public hostname in
`iss`, so set `KC_HOSTNAME` to the address users reach and use that same address for
`OPENSPOOR_OIDC_ISSUER`.

## Add a SAML identity provider (in Keycloak)

1. In the `openspoor` realm: Identity providers, Add provider, SAML v2.0.
2. Import the IdP's metadata URL or file, and give the IdP Keycloak's SP metadata
   (`<realm URL>/broker/<alias>/endpoint/descriptor`).
3. Add mappers on the provider:
   - an Attribute Importer from the SAML attribute that carries the tenant to the user attribute `tenant_id`
     (or a Hardcoded Attribute `tenant_id` when the IdP serves one tenant);
   - an Advanced Attribute to Role (or SAML Attribute to Role) mapper from the IdP's groups to the six realm roles.
4. Optional: set the provider as the default in the browser flow so users go straight to their IdP.

No Open Spoor configuration changes when a SAML provider is added: users still arrive with a
Keycloak-issued OIDC token.

## Check

Sign in through the console and call `GET /api/v1/me` with the access token. A 401 with
"token issuer is not the configured identity provider" means `iss` and `OPENSPOOR_OIDC_ISSUER`
differ (usually the hostname); "token audience does not include openspoor" means the audience
mapper is missing; "token has no tenant" means the `tenant_id` attribute is not set for the user.

-- +goose Up
-- Account security (OWASP ASVS V2, V3, V6): session revocation by token version, TOTP second factor with
-- recovery codes, e-mailed password recovery tokens, and sealed webhook secrets.

-- Every local token carries the user's token_version. Raising it (password or second-factor change,
-- sign-out, role or status change) invalidates all earlier tokens of that user.
alter table app_user
  add column token_version       integer not null default 0,
  add column password_changed_at timestamptz,
  -- TOTP secret, sealed with the tenant data key (AES-256-GCM); never stored in clear.
  add column mfa_secret          bytea,
  -- false while an enrolment is pending confirmation.
  add column mfa_enabled         boolean not null default false,
  add column mfa_enabled_at      timestamptz,
  -- last accepted TOTP time step; a code for that step or an earlier one is refused (replay).
  add column mfa_last_step       bigint not null default 0;

-- Single-use recovery codes. Only a keyed hash is stored.
create table user_recovery_code (
  id         uuid primary key,
  tenant_id  uuid not null references tenant(id) on delete cascade,
  user_id    uuid not null references app_user(id) on delete cascade,
  code_hash  text not null,
  used_at    timestamptz,
  created_at timestamptz not null default now(),
  unique (user_id, code_hash)
);
select enable_tenant_rls('user_recovery_code');

-- Password recovery tokens: random, single use, short-lived; only the SHA-256 of the token is stored.
create table password_reset (
  id         uuid primary key,
  tenant_id  uuid not null references tenant(id) on delete cascade,
  user_id    uuid not null references app_user(id) on delete cascade,
  token_hash text not null unique,
  expires_at timestamptz not null,
  used_at    timestamptz,
  created_at timestamptz not null default now()
);
create index password_reset_user_idx on password_reset (user_id);
select enable_tenant_rls('password_reset');

-- Webhook signing secrets are sealed with the tenant data key. Rows written before this migration keep
-- the clear text in "secret" until the application seals them (on first delivery, or `openspoor rotate-keys`).
alter table epcis_subscription alter column secret drop not null;
alter table epcis_subscription add column secret_sealed bytea;

-- +goose Down
alter table epcis_subscription drop column secret_sealed;
update epcis_subscription set secret = '' where secret is null;
alter table epcis_subscription alter column secret set not null;
drop table password_reset, user_recovery_code;
alter table app_user drop column token_version, drop column password_changed_at, drop column mfa_secret,
  drop column mfa_enabled, drop column mfa_enabled_at, drop column mfa_last_step;

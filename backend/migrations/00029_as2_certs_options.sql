-- +goose Up
-- AS2 certificate lifecycle (notBefore / subject, a second "next" certificate for rotation,
-- expiry alerts) and per-partner AS2 options (digest, cipher, compression, legacy flag, receipt
-- digest).

-- The station keeps its current certificate and, during a rotation, a next certificate with its
-- own sealed key. Signing switches to the next one at switch_at; until then both decrypt.
alter table as2_station
  add column not_before      timestamptz,
  add column subject         text not null default '',
  add column next_cert_pem   text,
  add column next_key_sealed bytea,
  add column next_fingerprint text,
  add column next_not_before timestamptz,
  add column next_not_after  timestamptz,
  add column next_subject    text,
  add column switch_at       timestamptz,
  add constraint as2_station_next_complete check (
    (next_cert_pem is null and next_key_sealed is null and next_fingerprint is null and next_not_before is null
      and next_not_after is null and next_subject is null and switch_at is null)
    or (next_cert_pem is not null and next_key_sealed is not null and next_fingerprint is not null and next_not_before is not null
      and next_not_after is not null and next_subject is not null and switch_at is not null));

-- A partner connection keeps its current certificate and, during a rotation, a next one: both verify
-- the partner's signatures; we encrypt to the next one from switch_at.
-- Options: signature_alg is the digest for messages we sign (and the integrity check we ask the
-- receipt to carry), mdn_signature_alg the digest that signs receipts we send, encryption the
-- content cipher, compress enables CMS compression. SHA-1 is allowed only with legacy.
alter table partner_as2
  add column not_before      timestamptz,
  add column subject         text not null default '',
  add column next_cert_sealed bytea,
  add column next_fingerprint text,
  add column next_not_before timestamptz,
  add column next_not_after  timestamptz,
  add column next_subject    text,
  add column switch_at       timestamptz,
  add column signature_alg   text not null default 'sha-256' check (signature_alg in ('sha-1', 'sha-256', 'sha-384', 'sha-512')),
  add column mdn_signature_alg text not null default 'sha-256' check (mdn_signature_alg in ('sha-1', 'sha-256', 'sha-384', 'sha-512')),
  add column encryption      text not null default 'aes-256-cbc' check (encryption in ('aes-256-cbc', 'aes-128-cbc')),
  add column compress        boolean not null default false,
  add column legacy          boolean not null default false,
  add constraint partner_as2_next_complete check (
    (next_cert_sealed is null and next_fingerprint is null and next_not_before is null and next_not_after is null
      and next_subject is null and switch_at is null)
    or (next_cert_sealed is not null and next_fingerprint is not null and next_not_before is not null and next_not_after is not null
      and next_subject is not null and switch_at is not null)),
  add constraint partner_as2_sha1_legacy check (legacy or (signature_alg <> 'sha-1' and mdn_signature_alg <> 'sha-1'));

-- The digest an outbound message's integrity check was computed with, so a receipt that arrives
-- later is checked against it.
alter table as2_message add column mic_alg text not null default 'sha-256' check (mic_alg in ('sha-1', 'sha-256', 'sha-384', 'sha-512'));

-- One row per certificate and expiry stage that has raised an alert, so a resolved alert is not
-- raised again by the next scheduled check.
create table as2_cert_alert (
  tenant_id   uuid not null references tenant(id) on delete cascade,
  scope       text not null check (scope in ('station', 'station_next', 'partner', 'partner_next')),
  subject_id  text not null,
  fingerprint text not null,
  stage       text not null check (stage in ('60d', '30d', '7d', 'expired')),
  raised_at   timestamptz not null default now(),
  primary key (tenant_id, scope, subject_id, fingerprint, stage)
);
select enable_tenant_rls('as2_cert_alert');

-- +goose Down
drop table as2_cert_alert;
alter table as2_message drop column mic_alg;
alter table partner_as2
  drop constraint partner_as2_sha1_legacy, drop constraint partner_as2_next_complete,
  drop column legacy, drop column compress, drop column encryption, drop column mdn_signature_alg, drop column signature_alg,
  drop column switch_at, drop column next_subject, drop column next_not_after, drop column next_not_before, drop column next_fingerprint,
  drop column next_cert_sealed, drop column subject, drop column not_before;
alter table as2_station
  drop constraint as2_station_next_complete,
  drop column switch_at, drop column next_subject, drop column next_not_after, drop column next_not_before, drop column next_fingerprint,
  drop column next_key_sealed, drop column next_cert_pem, drop column subject, drop column not_before;

-- +goose Up
-- Mapping studio: saved field mappings from partner files (CSV, JSON, fixed-width) to the
-- canonical B2B document or to EPCIS event fields.
create table b2b_mapping (
  id          uuid primary key,
  tenant_id   uuid not null references tenant(id) on delete cascade,
  name        text not null,
  partner_id  uuid references partner(id) on delete cascade,
  doc_type    text not null check (doc_type in ('order','asn','invoice','inventory','return','chargeback','epcis')),
  format      text not null check (format in ('csv','json','flat')),
  options     jsonb not null default '{}',
  fields      jsonb not null default '[]',
  created_at  timestamptz not null default now(),
  updated_at  timestamptz not null default now(),
  unique (tenant_id, name)
);
create index b2b_mapping_partner_idx on b2b_mapping (tenant_id, partner_id, doc_type);
select enable_tenant_rls('b2b_mapping');

-- Message tracking: functional acknowledgement of outbound documents, and the delivery job
-- behind each outbound federation message (attempts, last error and dead-letter state come
-- from the job queue rather than being copied).
alter table b2b_document add column acked_at timestamptz;
alter table federation_message add column job_id bigint;

create table partner_sla (
  tenant_id   uuid not null references tenant(id) on delete cascade,
  partner_id  uuid not null references partner(id) on delete cascade,
  ack_hours   int not null check (ack_hours between 1 and 720),
  enabled     boolean not null default true,
  updated_at  timestamptz not null default now(),
  primary key (tenant_id, partner_id)
);
select enable_tenant_rls('partner_sla');

-- Network directory: richer opt-in public profile with operator verification marks.
alter table directory_listing
  add column glns              text[] not null default '{}',
  add column company_prefixes  text[] not null default '{}',
  add column licenses          jsonb not null default '[]',
  add column contact           text not null default '',
  add column website           text not null default '',
  add column verified          boolean not null default false,
  add column verified_at       timestamptz,
  add column verified_by       text not null default '',
  add column verification_note text not null default '';

create table directory_connection_request (
  id          uuid primary key,
  tenant_id   uuid not null references tenant(id) on delete cascade,
  from_tenant uuid not null references tenant(id) on delete cascade,
  from_name   text not null,
  message     text not null default '',
  status      text not null default 'open',
  created_at  timestamptz not null default now()
);
select enable_tenant_rls('directory_connection_request');

-- Partner onboarding: checklist instantiated from a template, test runs, readiness score.
create table partner_onboarding (
  tenant_id   uuid not null references tenant(id) on delete cascade,
  partner_id  uuid not null references partner(id) on delete cascade,
  template    text not null,
  started_at  timestamptz not null default now(),
  primary key (tenant_id, partner_id)
);
select enable_tenant_rls('partner_onboarding');

create table partner_onboarding_item (
  id          uuid primary key,
  tenant_id   uuid not null references tenant(id) on delete cascade,
  partner_id  uuid not null references partner(id) on delete cascade,
  position    int not null,
  key         text not null,
  title       text not null,
  kind        text not null check (kind in ('manual','test')),
  channel     text not null default '',
  done        boolean not null default false,
  done_by     text not null default '',
  done_at     timestamptz,
  note        text not null default '',
  unique (tenant_id, partner_id, key)
);
select enable_tenant_rls('partner_onboarding_item');

create table partner_test_run (
  id          uuid primary key,
  tenant_id   uuid not null references tenant(id) on delete cascade,
  partner_id  uuid not null references partner(id) on delete cascade,
  channel     text not null,
  passed      boolean not null,
  detail      jsonb not null default '{}',
  created_at  timestamptz not null default now()
);
create index partner_test_run_idx on partner_test_run (tenant_id, partner_id, channel, created_at desc);
select enable_tenant_rls('partner_test_run');

-- Authorized trading partner credentials (W3C VC 2.0, eddsa-jcs-2022, Bitstring Status List).
create table atp_credential (
  id            uuid primary key,
  tenant_id     uuid not null references tenant(id) on delete cascade,
  license_id    uuid not null,
  status_index  int not null unique check (status_index >= 0 and status_index < 131072),
  credential    jsonb not null,
  valid_from    timestamptz not null,
  valid_until   timestamptz,
  revoked_at    timestamptz,
  revoked_reason text not null default '',
  created_at    timestamptz not null default now()
);
select enable_tenant_rls('atp_credential');

create table atp_verification (
  id            uuid primary key,
  tenant_id     uuid not null references tenant(id) on delete cascade,
  partner_id    uuid references partner(id) on delete cascade,
  credential_id text not null default '',
  issuer        text not null default '',
  subject       text not null default '',
  verified      boolean not null,
  checks        jsonb not null default '[]',
  credential    jsonb not null,
  checked_at    timestamptz not null default now()
);
create index atp_verification_partner_idx on atp_verification (tenant_id, partner_id, checked_at desc);
select enable_tenant_rls('atp_verification');

-- +goose Down
drop table atp_verification, atp_credential, partner_test_run, partner_onboarding_item, partner_onboarding,
  directory_connection_request, partner_sla, b2b_mapping cascade;
alter table directory_listing drop column glns, drop column company_prefixes, drop column licenses, drop column contact,
  drop column website, drop column verified, drop column verified_at, drop column verified_by, drop column verification_note;
alter table federation_message drop column job_id;
alter table b2b_document drop column acked_at;

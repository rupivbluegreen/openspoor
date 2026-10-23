-- +goose Up
-- EUDR readiness (per-tenant DDS role, application-date reference data, downstream references)
-- and cold-chain excursion dispositions.

-- The role this tenant plays for due diligence statements. 'operator' creates statements;
-- 'downstream_trader' holds a reference to the upstream statement instead.
-- TODO(spec): confirm the legal split of duties against the Official Journal text.
create table food_eudr_setting (
  tenant_id  uuid primary key references tenant(id) on delete cascade,
  dds_role   text not null default 'operator' check (dds_role in ('operator','downstream_trader')),
  updated_at timestamptz not null default now()
);
select enable_tenant_rls('food_eudr_setting');

-- Reference dates the tenant maintains. Defaults live in code and are shown as unverified
-- until a person confirms them against the Official Journal.
create table food_eudr_reference_date (
  tenant_id  uuid not null references tenant(id) on delete cascade,
  key        text not null,
  date       date not null,
  note       text not null default '',
  verified   boolean not null default false,
  updated_at timestamptz not null default now(),
  primary key (tenant_id, key)
);
select enable_tenant_rls('food_eudr_reference_date');

-- A downstream trader's record points at the upstream statement; it is not a new statement.
alter table eudr_dds
  add column kind text not null default 'statement' check (kind in ('statement','upstream_reference')),
  add column upstream_reference text not null default '',
  add column upstream_operator text not null default '';

-- One row per decision on a cold-chain excursion (append only; the latest row is current).
-- excursion_id refers to sensor_excursion (owned by the ecosystem module), without a foreign key.
create table food_excursion_disposition (
  id           uuid primary key,
  tenant_id    uuid not null references tenant(id) on delete cascade,
  excursion_id uuid not null,
  disposition  text not null check (disposition in ('quarantine','release','destroy')),
  reason       text not null,
  tlcs         text[] not null default '{}',
  prior_status jsonb not null default '{}',
  signature_id uuid,
  signed_by    text not null default '',
  meaning      text not null default '',
  decided_by   uuid not null,
  decided_at   timestamptz not null default now(),
  unique (signature_id)
);
create index food_excursion_disposition_idx on food_excursion_disposition (tenant_id, excursion_id, decided_at);
select enable_tenant_rls('food_excursion_disposition');

-- +goose Down
drop table food_excursion_disposition, food_eudr_reference_date, food_eudr_setting;
alter table eudr_dds drop column kind, drop column upstream_reference, drop column upstream_operator;

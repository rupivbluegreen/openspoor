-- +goose Up
alter table tenant add column quotas jsonb not null default '{}';
alter table tenant add column domain_token text;
alter table tenant add column domain_verified boolean not null default false;
alter table tenant add column support_until timestamptz;
alter table tenant add column support_level text not null default 'none';

create table usage_counter (
  tenant_id uuid not null references tenant(id) on delete cascade,
  period    text not null,              -- YYYY-MM
  metric    text not null,              -- epcis_events | reports | api_calls
  value     bigint not null default 0,
  billed    bigint not null default 0,
  primary key (tenant_id, period, metric)
);
select enable_tenant_rls('usage_counter');

-- Per-tenant data encryption key (envelope encryption under the master key).
-- Deleting the row crypto-shreds every secret sealed for the tenant.
create table tenant_key (
  tenant_id   uuid primary key references tenant(id) on delete cascade,
  wrapped_dek bytea not null,
  created_at  timestamptz not null default now()
);
select enable_tenant_rls('tenant_key');

-- +goose Down
drop table tenant_key, usage_counter;
alter table tenant drop column quotas, drop column domain_token, drop column domain_verified, drop column support_until, drop column support_level;

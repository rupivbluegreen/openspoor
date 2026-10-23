-- +goose Up
-- Certificates themselves live in the supplier compliance hub (supplier_record). The hub had no
-- field for what a certificate covers, so scope is added there (empty for existing records).
alter table supplier_record add column if not exists scope text not null default '';

-- Links a hub certificate (halal, origin, organic, ...) to a food lot (TLC), a product, or a
-- site (GLN: a supplier site or an own site). A lot is covered by certificates linked to the
-- lot itself, to its product, or to one of its sites.
create table food_certificate_link (
  id             uuid primary key,
  tenant_id      uuid not null references tenant(id) on delete cascade,
  certificate_id uuid not null references supplier_record(id) on delete cascade,
  target_type    text not null check (target_type in ('lot','product','site')),
  target         text not null,
  created_at     timestamptz not null default now(),
  unique (tenant_id, certificate_id, target_type, target)
);
create index food_certificate_link_target_idx on food_certificate_link (tenant_id, target_type, target);
select enable_tenant_rls('food_certificate_link');

-- One-up/one-down reports assembled for a competent authority (Regulation (EC) No 178/2002
-- Art. 18(2)-(3)). The report is kept as generated so later downloads are identical.
create table food_authority_request (
  id          uuid primary key,
  tenant_id   uuid not null references tenant(id) on delete cascade,
  authority   text not null,
  reference   text not null default '',
  reason      text not null default '',
  tlcs        text[] not null,
  period_from date,
  period_to   date,
  rows        int not null default 0,
  sha256      text not null,
  report      jsonb not null,
  created_by  text not null,
  exports     jsonb not null default '[]',
  created_at  timestamptz not null default now()
);
create index food_authority_request_recent_idx on food_authority_request (tenant_id, created_at desc);
select enable_tenant_rls('food_authority_request');

-- +goose Down
drop table food_authority_request, food_certificate_link cascade;
alter table supplier_record drop column if exists scope;

-- +goose Up
-- Traceability plan (21 CFR 1.1315): one row per saved version, per tenant and optionally per
-- site. Saving never updates a row; it inserts the next version and moves the "current" flag.
create table food_traceability_plan (
  id         uuid primary key,
  tenant_id  uuid not null references tenant(id) on delete cascade,
  site_id    uuid references site(id) on delete cascade,
  version    int not null check (version > 0),
  is_current boolean not null default true,
  content    jsonb not null,
  reason     text not null,
  created_by text not null,
  created_at timestamptz not null default now()
);
-- Versions are numbered per scope (tenant-wide, or one site).
create unique index food_traceability_plan_version_idx
  on food_traceability_plan (tenant_id, coalesce(site_id, '00000000-0000-0000-0000-000000000000'::uuid), version);
create unique index food_traceability_plan_current_idx
  on food_traceability_plan (tenant_id, coalesce(site_id, '00000000-0000-0000-0000-000000000000'::uuid)) where is_current;
select enable_tenant_rls('food_traceability_plan');

-- Optional review reminder. Helper UX only: the rule text read for this feature names no
-- review interval, so it is off by default.
create table food_traceability_plan_setting (
  tenant_id       uuid primary key references tenant(id) on delete cascade,
  review_reminder boolean not null default false,
  review_months   int not null default 12 check (review_months between 1 and 60),
  updated_at      timestamptz not null default now()
);
select enable_tenant_rls('food_traceability_plan_setting');

-- +goose Down
drop table food_traceability_plan_setting, food_traceability_plan cascade;

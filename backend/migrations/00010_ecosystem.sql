-- +goose Up
-- Marketplace listings are installation-wide (published by the operator or the community).
create table app_listing (
  id          text primary key,
  name        text not null,
  kind        text not null check (kind in ('connector','agent','rulepack','integration','app')),
  publisher   text not null,
  version     text not null,
  description text not null,
  manifest    jsonb not null,
  verified    boolean not null default false,
  created_at  timestamptz not null default now()
);

create table app_install (
  tenant_id   uuid not null references tenant(id) on delete cascade,
  app_id      text not null references app_listing(id),
  api_key_id  uuid,
  config      jsonb not null default '{}',
  installed_by text not null,
  created_at  timestamptz not null default now(),
  primary key (tenant_id, app_id)
);
select enable_tenant_rls('app_install');

create table sensor_reading (
  tenant_id   uuid not null,
  device_id   text not null,
  epc         text,
  shipment_id uuid,
  at          timestamptz not null,
  temp_c      numeric,
  humidity    numeric,
  primary key (tenant_id, device_id, at)
);
select enable_tenant_rls('sensor_reading');

create table sensor_excursion (
  id          uuid primary key,
  tenant_id   uuid not null references tenant(id) on delete cascade,
  device_id   text not null,
  epc         text,
  shipment_id uuid,
  min_c       numeric not null,
  max_c       numeric not null,
  started_at  timestamptz not null,
  ended_at    timestamptz not null,
  peak_c      numeric not null,
  minutes     int not null,
  status      text not null default 'open',
  created_at  timestamptz not null default now()
);
select enable_tenant_rls('sensor_excursion');

create table udi_device (
  id             uuid primary key,
  tenant_id      uuid not null references tenant(id) on delete cascade,
  di             text not null,
  brand_name     text not null,
  model          text not null default '',
  version        text not null default '',
  description    text not null default '',
  risk_class     text not null,
  gmdn_code      text not null default '',
  emdn_code      text not null default '',
  basic_udi_di   text not null default '',
  sterile        boolean not null default false,
  single_use     boolean not null default false,
  rx             boolean not null default true,
  labeler        text not null,
  submissions    jsonb not null default '{}',
  created_at     timestamptz not null default now(),
  unique (tenant_id, di)
);
select enable_tenant_rls('udi_device');

-- Measured performance runs (installation-wide, published by operators or CI).
create table benchmark_run (
  id          uuid primary key,
  measured_at timestamptz not null,
  environment text not null,
  version     text not null,
  results     jsonb not null,
  created_at  timestamptz not null default now()
);

-- +goose Down
drop table benchmark_run, udi_device, sensor_excursion, sensor_reading, app_install, app_listing cascade;

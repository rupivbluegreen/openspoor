-- +goose Up
create table partner (
  id             uuid primary key,
  tenant_id      uuid not null references tenant(id) on delete cascade,
  name           text not null,
  gln            text,
  company_prefix text,
  country        text,
  kind           text not null default 'wholesaler',
  status         text not null default 'pending',
  linked_tenant  uuid,
  endpoints      jsonb not null default '{}',
  credentials    bytea,
  master_data    jsonb not null default '{}',
  health         jsonb not null default '{}',
  created_at     timestamptz not null default now(),
  approved_at    timestamptz,
  unique (tenant_id, gln)
);
select enable_tenant_rls('partner');

create table partner_invitation (
  id           uuid primary key,
  tenant_id    uuid not null references tenant(id) on delete cascade,
  code         text not null unique,
  email        text not null,
  name         text not null,
  status       text not null default 'open',
  expires_at   timestamptz not null,
  accepted_by  uuid,
  created_at   timestamptz not null default now()
);
select enable_tenant_rls('partner_invitation');

-- Public partner directory: each tenant publishes one listing; search is cross-tenant (operator tx, published only).
create table directory_listing (
  tenant_id   uuid primary key references tenant(id) on delete cascade,
  name        text not null,
  gln         text,
  country     text,
  kind        text not null,
  description text not null default '',
  endpoints   jsonb not null default '{}',
  published   boolean not null default false,
  updated_at  timestamptz not null default now()
);
select enable_tenant_rls('directory_listing');

create table shipment (
  id              uuid primary key,
  tenant_id       uuid not null references tenant(id) on delete cascade,
  direction       text not null check (direction in ('outbound', 'inbound')),
  partner_id      uuid references partner(id),
  po_number       text not null default '',
  asn_number      text not null default '',
  ship_date       date,
  status          text not null default 'draft',
  epcs            text[] not null default '{}',
  asn             jsonb not null default '{}',
  tits            jsonb,
  transport       text not null default 'https',
  response        jsonb,
  reconciliation  jsonb,
  capture_id      uuid,
  sent_at         timestamptz,
  received_at     timestamptz,
  created_at      timestamptz not null default now()
);
create index shipment_recent_idx on shipment (tenant_id, created_at desc);
select enable_tenant_rls('shipment');

create table vrs_directory (
  id            uuid primary key,
  tenant_id     uuid not null references tenant(id) on delete cascade,
  gtin_prefix   text not null,
  responder_url text not null,
  responder_gln text,
  credentials   bytea,
  created_at    timestamptz not null default now(),
  unique (tenant_id, gtin_prefix)
);
select enable_tenant_rls('vrs_directory');

create table vrs_request (
  id            uuid primary key,
  tenant_id     uuid not null references tenant(id) on delete cascade,
  direction     text not null check (direction in ('outbound', 'inbound')),
  corr_uuid     uuid not null,
  gtin          text not null,
  serial        text not null,
  lot           text not null default '',
  expiry        text not null default '',
  requestor_gln text not null default '',
  context       text not null default '',
  verified      boolean,
  reason        text not null default '',
  responder     text not null default '',
  latency_ms    int not null default 0,
  response      jsonb,
  created_at    timestamptz not null default now()
);
create index vrs_request_recent_idx on vrs_request (tenant_id, created_at desc);
select enable_tenant_rls('vrs_request');

create table retention_policy (
  tenant_id  uuid not null references tenant(id) on delete cascade,
  data_class text not null,
  years      int not null check (years between 1 and 30),
  basis      text not null default '',
  primary key (tenant_id, data_class)
);
select enable_tenant_rls('retention_policy');

-- +goose Down
drop table retention_policy, vrs_request, vrs_directory, shipment, directory_listing, partner_invitation, partner cascade;

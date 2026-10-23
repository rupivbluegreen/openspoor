-- +goose Up
-- Consumer-facing information the brand owner publishes per GTIN and language.
create table consumer_info (
  tenant_id    uuid not null references tenant(id) on delete cascade,
  gtin         text not null check (gtin ~ '^[0-9]{14}$'),
  lang         text not null check (lang ~ '^[a-z]{2}$'),
  leaflet_url  text not null default '',
  leaflet_text text not null default '',
  allergens    text[] not null default '{}',
  origin       text not null default '',
  certificates jsonb not null default '[]',
  updated_at   timestamptz not null default now(),
  primary key (tenant_id, gtin, lang)
);
select enable_tenant_rls('consumer_info');

-- Anonymous scan counts: no scanner identity, address or device is stored.
create table scan_stat (
  tenant_id uuid not null references tenant(id) on delete cascade,
  gtin      text not null,
  day       date not null,
  outcome   text not null,
  count     bigint not null default 0,
  primary key (tenant_id, gtin, day, outcome)
);
select enable_tenant_rls('scan_stat');

-- Suspect-product reports from the public page; no personal data.
create table consumer_report (
  id         uuid primary key,
  tenant_id  uuid not null references tenant(id) on delete cascade,
  gtin       text not null,
  serial     text not null default '',
  lot        text not null default '',
  reason     text not null,
  details    text not null default '',
  country    text not null default '',
  created_at timestamptz not null default now()
);
create index consumer_report_recent_idx on consumer_report (tenant_id, created_at desc);
select enable_tenant_rls('consumer_report');

-- Opt-in recall notices: kept only until the notice is sent or the subscription expires.
create table recall_subscription (
  id          uuid primary key,
  tenant_id   uuid not null references tenant(id) on delete cascade,
  gtin        text not null,
  lot         text not null,
  email       text not null,
  token       text not null unique,
  lang        text not null default 'en',
  consent_at  timestamptz not null default now(),
  expires_at  timestamptz not null,
  unique (tenant_id, gtin, lot, email)
);
select enable_tenant_rls('recall_subscription');

-- +goose Down
drop table recall_subscription;
drop table consumer_report;
drop table scan_stat;
drop table consumer_info;

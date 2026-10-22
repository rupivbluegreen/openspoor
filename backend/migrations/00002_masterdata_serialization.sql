-- +goose Up
create table product (
  id                    uuid primary key,
  tenant_id             uuid not null references tenant(id) on delete cascade,
  gtin                  text not null check (gtin ~ '^[0-9]{14}$'),
  name                  text not null,
  description           text not null default '',
  company_prefix_length int not null default 7 check (company_prefix_length between 6 and 12),
  ndc                   text,
  market_codes          jsonb not null default '{}',
  target_markets        text[] not null default '{}',
  dosage_form           text not null default '',
  strength              text not null default '',
  pack_size             text not null default '',
  manufacturer_org_id   uuid references organization(id) on delete set null,
  vertical              text not null default 'pharma',
  status                text not null default 'active',
  attributes            jsonb not null default '{}',
  created_at            timestamptz not null default now(),
  updated_at            timestamptz not null default now(),
  unique (tenant_id, gtin)
);
select enable_tenant_rls('product');

create table packaging_template (
  id         uuid primary key,
  tenant_id  uuid not null references tenant(id) on delete cascade,
  product_id uuid not null references product(id) on delete cascade,
  market     text not null,
  levels     jsonb not null,
  created_at timestamptz not null default now(),
  updated_at timestamptz not null default now(),
  unique (tenant_id, product_id, market)
);
select enable_tenant_rls('packaging_template');

create table serial_pool (
  id                uuid primary key,
  tenant_id         uuid not null references tenant(id) on delete cascade,
  product_id        uuid not null references product(id),
  gtin              text not null,
  market            text not null,
  mode              text not null check (mode in ('random', 'sequential')),
  alphabet          text not null,
  length            int not null check (length between 4 and 20),
  prefix            text not null default '',
  key_material      bytea not null,
  next_counter      bigint not null default 0,
  capacity          bigint not null,
  low_watermark_pct int not null default 10,
  status            text not null default 'active',
  issued            bytea,
  commissioned      bytea,
  created_at        timestamptz not null default now(),
  unique (tenant_id, gtin, market, prefix)
);
select enable_tenant_rls('serial_pool');

create table serial_allocation (
  id              uuid primary key,
  tenant_id       uuid not null references tenant(id) on delete cascade,
  pool_id         uuid not null references serial_pool(id),
  idempotency_key text not null,
  start_counter   bigint not null,
  count           int not null,
  requested_by    text not null,
  created_at      timestamptz not null default now(),
  unique (tenant_id, pool_id, idempotency_key)
);
select enable_tenant_rls('serial_allocation');

-- +goose Down
drop table serial_allocation, serial_pool, packaging_template, product cascade;

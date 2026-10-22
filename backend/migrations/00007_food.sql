-- +goose Up
create table food_lot (
  id            uuid primary key,
  tenant_id     uuid not null references tenant(id) on delete cascade,
  tlc           text not null,
  tlc_source    text not null default '',
  product_id    uuid references product(id) on delete set null,
  gtin          text,
  description   text not null,
  quantity      numeric not null default 0,
  uom           text not null default 'EA',
  location_gln  text,
  origin        text not null default 'initially_packed',
  harvest_date  date,
  pack_date     date,
  plot_ids      uuid[] not null default '{}',
  status        text not null default 'active',
  kdes          jsonb not null default '{}',
  created_at    timestamptz not null default now(),
  unique (tenant_id, tlc)
);
select enable_tenant_rls('food_lot');

create table food_cte (
  id            uuid primary key,
  tenant_id     uuid not null references tenant(id) on delete cascade,
  type          text not null check (type in ('harvesting','cooling','initial_packing','first_land_based_receiving','shipping','receiving','transformation')),
  tlc           text not null,
  quantity      numeric not null default 0,
  uom           text not null default 'EA',
  event_time    timestamptz not null,
  location_gln  text not null default '',
  location_name text not null default '',
  ship_from     text not null default '',
  ship_to       text not null default '',
  ship_to_name  text not null default '',
  ref_doc_type  text not null default '',
  ref_doc_number text not null default '',
  kdes          jsonb not null default '{}',
  epcis_capture uuid,
  created_at    timestamptz not null default now()
);
create index food_cte_tlc_idx on food_cte (tenant_id, tlc);
create index food_cte_time_idx on food_cte (tenant_id, event_time);
select enable_tenant_rls('food_cte');

-- input lot -> output lot links created by transformation CTEs
create table food_lineage (
  tenant_id  uuid not null references tenant(id) on delete cascade,
  input_tlc  text not null,
  output_tlc text not null,
  quantity   numeric not null default 0,
  cte_id     uuid not null,
  primary key (tenant_id, input_tlc, output_tlc, cte_id)
);
select enable_tenant_rls('food_lineage');

create table farm_plot (
  id            uuid primary key,
  tenant_id     uuid not null references tenant(id) on delete cascade,
  name          text not null,
  producer_name text not null default '',
  country       text not null,
  commodity     text not null,
  area_ha       numeric not null default 0,
  geojson       jsonb not null,
  deforestation_free boolean,
  created_at    timestamptz not null default now()
);
select enable_tenant_rls('farm_plot');

create table recall (
  id             uuid primary key,
  tenant_id      uuid not null references tenant(id) on delete cascade,
  title          text not null,
  reason         text not null,
  classification text not null default 'II',
  status         text not null default 'open',
  tlcs           text[] not null default '{}',
  pharma_lots    jsonb not null default '[]',
  affected       jsonb not null default '{}',
  created_at     timestamptz not null default now(),
  closed_at      timestamptz
);
select enable_tenant_rls('recall');

create table recall_notification (
  id            uuid primary key,
  tenant_id     uuid not null references tenant(id) on delete cascade,
  recall_id     uuid not null references recall(id) on delete cascade,
  customer_name text not null,
  customer_gln  text not null default '',
  channel       text not null,
  status        text not null default 'sent',
  sent_at       timestamptz not null default now(),
  acknowledged_at timestamptz
);
select enable_tenant_rls('recall_notification');

create table eudr_dds (
  id                  uuid primary key,
  tenant_id           uuid not null references tenant(id) on delete cascade,
  reference           text not null,
  operator_role       text not null default 'operator',
  commodity           text not null,
  hs_code             text not null,
  description         text not null,
  quantity            numeric not null,
  uom                 text not null default 'KGM',
  country_of_production text not null,
  plot_ids            uuid[] not null,
  status              text not null default 'draft',
  payload             jsonb not null default '{}',
  submission_ref      text not null default '',
  created_at          timestamptz not null default now(),
  unique (tenant_id, reference)
);
select enable_tenant_rls('eudr_dds');

-- +goose Down
drop table eudr_dds, recall_notification, recall, farm_plot, food_lineage, food_cte, food_lot cascade;

-- +goose Up
-- AI-native agents: per-tool autonomy ladder, mapping samples and regression cases,
-- inspection packages (draft until a person signs) and watched regulatory sources.

-- Autonomy per action type (tool) and tenant: suggest = a person applies it manually,
-- approve = applied after approval (default), auto = applied automatically with audit and undo
-- (the application only accepts auto for low-risk reversible tools).
create table tool_autonomy (
  tenant_id  uuid not null references tenant(id) on delete cascade,
  tool       text not null,
  mode       text not null check (mode in ('suggest','approve','auto')),
  updated_by text not null default '',
  updated_at timestamptz not null default now(),
  primary key (tenant_id, tool)
);
select enable_tenant_rls('tool_autonomy');

-- Sample partner files the mapping agent reads (uploaded, or kept from a failed import).
create table mapping_sample (
  id         uuid primary key,
  tenant_id  uuid not null references tenant(id) on delete cascade,
  name       text not null,
  partner_id uuid references partner(id) on delete set null,
  doc_type   text not null check (doc_type in ('order','asn','invoice','inventory','return','chargeback','epcis')),
  format     text not null default 'unknown' check (format in ('csv','json','flat','xml','edi','unknown')),
  options    jsonb not null default '{}',
  content    text not null,
  source     text not null default 'upload' check (source in ('upload','failed_import')),
  error      text not null default '',
  status     text not null default 'new' check (status in ('new','proposed','mapped','unsupported','dismissed')),
  mapping_id uuid references b2b_mapping(id) on delete set null,
  created_by text not null default '',
  created_at timestamptz not null default now()
);
create index mapping_sample_status_idx on mapping_sample (tenant_id, status, created_at);
select enable_tenant_rls('mapping_sample');

-- Regression cases stored with a saved mapping: a sample row and the canonical values expected.
create table mapping_case (
  id         uuid primary key,
  tenant_id  uuid not null references tenant(id) on delete cascade,
  mapping_id uuid not null references b2b_mapping(id) on delete cascade,
  sample_id  uuid,
  position   int not null default 0,
  row        jsonb not null,
  expected   jsonb not null,
  created_at timestamptz not null default now()
);
create index mapping_case_idx on mapping_case (tenant_id, mapping_id, position);
select enable_tenant_rls('mapping_case');

-- Inspection packages: assembled by the inspection agent as drafts; released only with a
-- person's electronic signature. Nothing is sent to an authority from here.
create table inspection_package (
  id           uuid primary key,
  tenant_id    uuid not null references tenant(id) on delete cascade,
  kind         text not null check (kind in ('fsma204','tracing')),
  title        text not null,
  scope        jsonb not null default '{}',
  status       text not null default 'draft' check (status in ('draft','released','voided')),
  content_type text not null default 'text/csv',
  content      text not null default '',
  sha256       text not null default '',
  summary      jsonb not null default '{}',
  checklist    jsonb not null default '[]',
  created_by   text not null default '',
  created_at   timestamptz not null default now(),
  updated_at   timestamptz not null default now(),
  signature_id uuid,
  released_by  text not null default '',
  released_at  timestamptz
);
create index inspection_package_idx on inspection_package (tenant_id, created_at desc);
select enable_tenant_rls('inspection_package');

-- Watched regulator pages and specifications.
create table watch_source (
  id              uuid primary key,
  tenant_id       uuid not null references tenant(id) on delete cascade,
  name            text not null,
  url             text not null,
  selector        text not null default '',
  market          text not null default '',
  enabled         boolean not null default true,
  last_hash       text not null default '',
  last_text       text not null default '',
  last_checked_at timestamptz,
  last_changed_at timestamptz,
  last_error      text not null default '',
  created_at      timestamptz not null default now(),
  unique (tenant_id, name)
);
select enable_tenant_rls('watch_source');

-- +goose Down
drop table watch_source, inspection_package, mapping_case, mapping_sample, tool_autonomy cascade;

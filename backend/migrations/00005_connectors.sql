-- +goose Up
create table connector_instance (
  id         uuid primary key,
  tenant_id  uuid not null references tenant(id) on delete cascade,
  connector  text not null,
  name       text not null,
  config     jsonb not null default '{}',
  secrets    bytea,
  enabled    boolean not null default true,
  created_at timestamptz not null default now()
);
select enable_tenant_rls('connector_instance');

create table regulatory_report (
  id             uuid primary key,
  tenant_id      uuid not null references tenant(id) on delete cascade,
  instance_id    uuid not null references connector_instance(id) on delete cascade,
  connector      text not null,
  kind           text not null,
  idem_key       text not null,
  payload        jsonb not null,
  packs          jsonb not null default '[]',
  status         text not null default 'queued',
  submission_ref text not null default '',
  attempts       int not null default 0,
  response       jsonb,
  rejection      jsonb,
  alert          jsonb,
  capture_id     uuid,
  event_id       text not null default '',
  created_at     timestamptz not null default now(),
  updated_at     timestamptz not null default now(),
  unique (tenant_id, instance_id, idem_key)
);
create index regulatory_report_recent_idx on regulatory_report (tenant_id, created_at desc);
select enable_tenant_rls('regulatory_report');

-- +goose Down
drop table regulatory_report, connector_instance cascade;

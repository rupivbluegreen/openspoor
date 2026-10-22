-- +goose Up
-- +goose StatementBegin
create or replace function app_tenant() returns uuid language sql stable as $$
  select nullif(current_setting('app.tenant_id', true), '')::uuid
$$;
create or replace function app_operator() returns boolean language sql stable as $$
  select coalesce(current_setting('app.operator', true), '') = 'on'
$$;
-- Enables RLS (forced, so the table owner is also restricted) and a tenant policy.
create or replace function enable_tenant_rls(tbl regclass) returns void language plpgsql as $f$
begin
  execute format('alter table %s enable row level security', tbl);
  execute format('alter table %s force row level security', tbl);
  execute format('create policy tenant_isolation on %s using (tenant_id = app_tenant() or app_operator()) with check (tenant_id = app_tenant() or app_operator())', tbl);
end
$f$;
-- +goose StatementEnd

create table tenant (
  id            uuid primary key,
  slug          text not null unique check (slug ~ '^[a-z0-9][a-z0-9-]{1,40}$'),
  name          text not null,
  plan          text not null default 'community',
  status        text not null default 'active',
  region        text not null default 'eu',
  verticals     text[] not null default '{pharma}',
  brand         jsonb not null default '{}',
  settings      jsonb not null default '{}',
  custom_domain text unique,
  created_at    timestamptz not null default now()
);
alter table tenant enable row level security;
alter table tenant force row level security;
create policy tenant_self on tenant using (id = app_tenant() or app_operator()) with check (id = app_tenant() or app_operator());

create table organization (
  id             uuid primary key,
  tenant_id      uuid not null references tenant(id) on delete cascade,
  name           text not null,
  kind           text not null default 'manufacturer',
  company_prefix text,
  country        text,
  created_at     timestamptz not null default now()
);
select enable_tenant_rls('organization');

create table site (
  id         uuid primary key,
  tenant_id  uuid not null references tenant(id) on delete cascade,
  org_id     uuid not null references organization(id) on delete cascade,
  gln        text not null check (gln ~ '^[0-9]{13}$'),
  name       text not null,
  kind       text not null default 'plant',
  country    text,
  address    jsonb not null default '{}',
  created_at timestamptz not null default now(),
  unique (tenant_id, gln)
);
select enable_tenant_rls('site');

create table license (
  id         uuid primary key,
  tenant_id  uuid not null references tenant(id) on delete cascade,
  org_id     uuid not null references organization(id) on delete cascade,
  site_id    uuid references site(id) on delete set null,
  kind       text not null,
  number     text not null,
  authority  text not null,
  country    text,
  valid_from date,
  valid_to   date,
  created_at timestamptz not null default now()
);
select enable_tenant_rls('license');

create table app_user (
  id            uuid primary key,
  tenant_id     uuid not null references tenant(id) on delete cascade,
  email         text not null,
  name          text not null default '',
  password_hash text,
  roles         text[] not null default '{Viewer}',
  status        text not null default 'active',
  operator      boolean not null default false,
  external_id   text,
  locale        text not null default 'en',
  created_at    timestamptz not null default now(),
  unique (tenant_id, email)
);
select enable_tenant_rls('app_user');

create table api_key (
  id           uuid primary key,
  tenant_id    uuid not null references tenant(id) on delete cascade,
  name         text not null,
  prefix       text not null,
  hash         text not null unique,
  roles        text[] not null,
  created_by   text not null,
  expires_at   timestamptz,
  revoked_at   timestamptz,
  last_used_at timestamptz,
  created_at   timestamptz not null default now()
);
select enable_tenant_rls('api_key');

create table audit_entry (
  id        bigserial primary key,
  tenant_id uuid not null,
  seq       bigint not null,
  at        timestamptz not null,
  actor     text not null,
  action    text not null,
  entity    text not null,
  entity_id text not null,
  before    jsonb,
  after     jsonb,
  reason    text not null default '',
  prev_hash text not null,
  hash      text not null,
  unique (tenant_id, seq)
);
create index audit_entity_idx on audit_entry (tenant_id, entity, entity_id);
select enable_tenant_rls('audit_entry');

-- +goose StatementBegin
create or replace function audit_immutable() returns trigger language plpgsql as $$
begin
  raise exception 'audit_entry is append-only';
end
$$;
-- +goose StatementEnd
create trigger audit_no_update before update or delete on audit_entry for each row execute function audit_immutable();
create trigger audit_no_truncate before truncate on audit_entry for each statement execute function audit_immutable();

create table esignature (
  id         uuid primary key,
  tenant_id  uuid not null references tenant(id) on delete cascade,
  user_id    uuid not null,
  email      text not null,
  meaning    text not null,
  reason     text not null,
  entity     text not null,
  entity_id  text not null,
  audit_seq  bigint not null,
  signed_at  timestamptz not null default now()
);
select enable_tenant_rls('esignature');

-- System queue: no RLS, never exposed directly to tenants.
create table job (
  id           bigserial primary key,
  tenant_id    uuid,
  kind         text not null,
  payload      jsonb not null,
  status       text not null default 'queued',
  attempts     int not null default 0,
  max_attempts int not null default 8,
  last_error   text not null default '',
  dedupe_key   text,
  run_at       timestamptz not null default now(),
  created_at   timestamptz not null default now(),
  updated_at   timestamptz not null default now()
);
create index job_ready_idx on job (run_at, id) where status = 'queued';
create unique index job_dedupe_idx on job (dedupe_key) where dedupe_key is not null and status in ('queued', 'running');

create table alert (
  id          uuid primary key,
  tenant_id   uuid not null references tenant(id) on delete cascade,
  kind        text not null,
  severity    text not null default 'warning',
  title       text not null,
  detail      jsonb not null default '{}',
  status      text not null default 'open',
  entity      text not null default '',
  entity_id   text not null default '',
  dedupe_key  text,
  created_at  timestamptz not null default now(),
  resolved_at timestamptz
);
create unique index alert_dedupe_idx on alert (tenant_id, dedupe_key) where dedupe_key is not null and status = 'open';
create index alert_open_idx on alert (tenant_id, status, created_at desc);
select enable_tenant_rls('alert');

-- +goose Down
drop table alert, job, esignature, audit_entry, api_key, app_user, license, site, organization, tenant cascade;
drop function audit_immutable, enable_tenant_rls, app_operator, app_tenant;

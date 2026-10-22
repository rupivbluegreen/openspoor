-- +goose Up
create table agent_setting (
  tenant_id uuid not null references tenant(id) on delete cascade,
  agent     text not null,
  enabled   boolean not null default true,
  autonomy  text not null default 'propose' check (autonomy in ('propose','auto_low_risk')),
  primary key (tenant_id, agent)
);
select enable_tenant_rls('agent_setting');

create table agent_action (
  id           uuid primary key,
  tenant_id    uuid not null references tenant(id) on delete cascade,
  agent        text not null,
  tool         text not null,
  title        text not null,
  summary      text not null default '',
  reasoning    jsonb not null default '[]',
  input        jsonb not null default '{}',
  before       jsonb,
  after        jsonb,
  risk         text not null default 'low',
  confidence   numeric not null default 0.5,
  status       text not null default 'proposed',
  entity       text not null default '',
  entity_id    text not null default '',
  dedupe_key   text,
  decided_by   text,
  decided_at   timestamptz,
  executed_at  timestamptz,
  reverted_at  timestamptz,
  result       jsonb,
  created_at   timestamptz not null default now()
);
create unique index agent_action_dedupe_idx on agent_action (tenant_id, dedupe_key) where dedupe_key is not null and status in ('proposed','approved','executed');
create index agent_action_status_idx on agent_action (tenant_id, status, created_at desc);
select enable_tenant_rls('agent_action');

create table work_case (
  id          uuid primary key,
  tenant_id   uuid not null references tenant(id) on delete cascade,
  kind        text not null,
  title       text not null,
  status      text not null default 'open',
  priority    int not null default 3 check (priority between 1 and 4),
  score       numeric not null default 0,
  assignee    text not null default '',
  entity      text not null default '',
  entity_id   text not null default '',
  source      text not null default '',
  source_id   text not null default '',
  notes       jsonb not null default '[]',
  due_at      timestamptz,
  created_at  timestamptz not null default now(),
  updated_at  timestamptz not null default now()
);
create unique index work_case_source_idx on work_case (tenant_id, source, source_id) where source_id <> '' and status <> 'resolved';
select enable_tenant_rls('work_case');

create table form_def (
  id         uuid primary key,
  tenant_id  uuid not null references tenant(id) on delete cascade,
  name       text not null,
  schema     jsonb not null,
  created_at timestamptz not null default now(),
  unique (tenant_id, name)
);
select enable_tenant_rls('form_def');

create table form_submission (
  id         uuid primary key,
  tenant_id  uuid not null references tenant(id) on delete cascade,
  form_id    uuid not null references form_def(id) on delete cascade,
  data       jsonb not null,
  submitted_by text not null,
  created_at timestamptz not null default now()
);
select enable_tenant_rls('form_submission');

create table workflow_def (
  id          uuid primary key,
  tenant_id   uuid not null references tenant(id) on delete cascade,
  key         text not null,
  name        text not null,
  definition  jsonb not null,
  builtin     boolean not null default false,
  created_at  timestamptz not null default now(),
  unique (tenant_id, key)
);
select enable_tenant_rls('workflow_def');

create table workflow_run (
  id           uuid primary key,
  tenant_id    uuid not null references tenant(id) on delete cascade,
  workflow_id  uuid not null references workflow_def(id) on delete cascade,
  status       text not null default 'running',
  step         int not null default 0,
  context      jsonb not null default '{}',
  history      jsonb not null default '[]',
  started_by   text not null,
  created_at   timestamptz not null default now(),
  updated_at   timestamptz not null default now()
);
select enable_tenant_rls('workflow_run');

-- +goose Down
drop table workflow_run, workflow_def, form_submission, form_def, work_case, agent_action, agent_setting cascade;

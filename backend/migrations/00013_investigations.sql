-- +goose Up
-- DSCSA tracing requests and suspect / illegitimate product investigations.
create table investigation (
  id                   uuid primary key,
  tenant_id            uuid not null references tenant(id) on delete cascade,
  kind                 text not null check (kind in ('tracing_request', 'suspect', 'illegitimate')),
  status               text not null default 'open'
                       check (status in ('open', 'quarantined', 'investigating', 'cleared', 'confirmed_illegitimate', 'closed')),
  title                text not null default '',
  gtin                 text not null default '',
  serials              text[] not null default '{}',
  lots                 text[] not null default '{}',
  -- Identifiers in scope, resolved against serial_state (canonical form, including descendants).
  epcs                 text[] not null default '{}',
  not_found            text[] not null default '{}',
  shipment_id          uuid references shipment(id),
  requester_type       text not null default '' check (requester_type in ('', 'partner', 'authority')),
  requester_partner_id uuid references partner(id),
  requester_name       text not null default '',
  request_ref          text not null default '',
  source_alert_id      uuid,
  due_at               timestamptz,
  -- True while the identifiers are quarantined through an EPCIS holding event.
  held                 boolean not null default false,
  findings             jsonb not null default '[]',
  decision             text not null default '',
  tracing_answer       jsonb,
  answered_at          timestamptz,
  signature_id         uuid,
  created_by           text not null default '',
  created_at           timestamptz not null default now(),
  updated_at           timestamptz not null default now(),
  closed_at            timestamptz
);
create index investigation_recent_idx on investigation (tenant_id, created_at desc);
create index investigation_held_idx on investigation using gin (epcs) where held;
select enable_tenant_rls('investigation');

create table investigation_event (
  id               uuid primary key,
  tenant_id        uuid not null references tenant(id) on delete cascade,
  investigation_id uuid not null references investigation(id) on delete cascade,
  kind             text not null,
  from_status      text not null default '',
  to_status        text not null default '',
  actor            text not null default '',
  note             text not null default '',
  data             jsonb not null default '{}',
  created_at       timestamptz not null default now()
);
create index investigation_event_idx on investigation_event (tenant_id, investigation_id, created_at);
select enable_tenant_rls('investigation_event');

-- Per-tenant response windows (hours) by investigation kind; 0 means no due date.
create table investigation_policy (
  tenant_id  uuid not null references tenant(id) on delete cascade,
  kind       text not null check (kind in ('tracing_request', 'suspect', 'illegitimate')),
  due_hours  int not null check (due_hours between 0 and 8760),
  basis      text not null default '',
  primary key (tenant_id, kind)
);
select enable_tenant_rls('investigation_policy');

-- +goose Down
drop table investigation_policy, investigation_event, investigation cascade;

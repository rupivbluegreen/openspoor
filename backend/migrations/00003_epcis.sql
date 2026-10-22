-- +goose Up
create table epcis_capture (
  id           uuid primary key,
  tenant_id    uuid not null references tenant(id) on delete cascade,
  status       text not null,
  format       text not null,
  event_count  int not null default 0,
  error_count  int not null default 0,
  errors       jsonb not null default '[]',
  source       text not null default '',
  archive_key  text,
  created_by   text not null,
  created_at   timestamptz not null default now(),
  finished_at  timestamptz
);
create index epcis_capture_recent_idx on epcis_capture (tenant_id, created_at desc);
select enable_tenant_rls('epcis_capture');

create table epcis_event (
  id               uuid not null,
  tenant_id        uuid not null,
  capture_id       uuid not null,
  event_id         text not null,
  event_type       text not null,
  action           text,
  biz_step         text,
  disposition      text,
  event_time       timestamptz not null,
  event_tz         text not null default '+00:00',
  record_time      timestamptz not null,
  read_point       text,
  biz_location     text,
  parent_id        text,
  epcs             text[] not null default '{}',
  quantities       jsonb,
  biz_transactions jsonb,
  source_dest      jsonb,
  ilmd             jsonb,
  doc              jsonb not null,
  primary key (tenant_id, record_time, id)
) partition by range (record_time);
create table epcis_event_default partition of epcis_event default;
create index epcis_event_epcs_idx on epcis_event using gin (epcs);
create index epcis_event_time_idx on epcis_event (tenant_id, event_time);
create index epcis_event_capture_idx on epcis_event (tenant_id, capture_id);
create index epcis_event_eventid_idx on epcis_event (tenant_id, event_id);
select enable_tenant_rls('epcis_event');

-- +goose StatementBegin
-- Creates the monthly partition holding ts if it does not exist yet.
create or replace function ensure_epcis_partition(ts timestamptz) returns void language plpgsql as $$
declare
  start_m date := date_trunc('month', ts at time zone 'UTC')::date;
  name text := format('epcis_event_%s', to_char(start_m, 'YYYYMM'));
begin
  if to_regclass(name) is null then
    begin
      execute format('create table %I partition of epcis_event for values from (%L) to (%L)',
        name, start_m::timestamp at time zone 'UTC', (start_m + interval '1 month')::timestamp at time zone 'UTC');
      execute format('alter table %I enable row level security', name);
      execute format('alter table %I force row level security', name);
    exception when duplicate_table then null;
    end;
  end if;
end
$$;
-- +goose StatementEnd

create table serial_state (
  tenant_id       uuid not null,
  epc             text not null,
  gtin            text,
  serial          text,
  lot             text,
  expiry          date,
  status          text not null,
  disposition     text,
  biz_location    text,
  parent_epc      text,
  last_event_time timestamptz not null,
  last_event_id   text not null,
  updated_at      timestamptz not null default now(),
  primary key (tenant_id, epc)
) partition by hash (tenant_id);
create table serial_state_p0 partition of serial_state for values with (modulus 8, remainder 0);
create table serial_state_p1 partition of serial_state for values with (modulus 8, remainder 1);
create table serial_state_p2 partition of serial_state for values with (modulus 8, remainder 2);
create table serial_state_p3 partition of serial_state for values with (modulus 8, remainder 3);
create table serial_state_p4 partition of serial_state for values with (modulus 8, remainder 4);
create table serial_state_p5 partition of serial_state for values with (modulus 8, remainder 5);
create table serial_state_p6 partition of serial_state for values with (modulus 8, remainder 6);
create table serial_state_p7 partition of serial_state for values with (modulus 8, remainder 7);
create index serial_state_parent_idx on serial_state (tenant_id, parent_epc) where parent_epc is not null;
select enable_tenant_rls('serial_state');

create table epcis_named_query (
  id         uuid primary key,
  tenant_id  uuid not null references tenant(id) on delete cascade,
  name       text not null,
  query      jsonb not null,
  created_at timestamptz not null default now(),
  unique (tenant_id, name)
);
select enable_tenant_rls('epcis_named_query');

create table epcis_subscription (
  id               uuid primary key,
  tenant_id        uuid not null references tenant(id) on delete cascade,
  query_name       text,
  query            jsonb not null default '{}',
  dest             text not null,
  secret           text not null,
  active           boolean not null default true,
  delivered_count  bigint not null default 0,
  last_error       text not null default '',
  created_at       timestamptz not null default now()
);
select enable_tenant_rls('epcis_subscription');

-- +goose Down
drop table epcis_subscription, epcis_named_query, serial_state, epcis_event, epcis_capture cascade;
drop function ensure_epcis_partition;

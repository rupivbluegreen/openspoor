-- +goose Up
-- Mass-balance reconciliation, pharma lot recall, Digital Link resolver links and
-- tamper-evidence checkpoints. All tables are tenant-scoped (row level security).

-- One row per tenant: reconciliation tolerance and the checkpoint cadence.
create table trace_setting (
  tenant_id         uuid primary key references tenant(id) on delete cascade,
  tolerance_units   int not null default 0 check (tolerance_units between 0 and 1000000),
  tolerance_percent numeric(6,3) not null default 0 check (tolerance_percent between 0 and 100),
  propose_case      boolean not null default false,
  balance_auto      boolean not null default false,
  -- Hours between automatic signed checkpoints; 0 turns them off.
  checkpoint_hours  int not null default 24 check (checkpoint_hours between 0 and 8760),
  updated_by        text not null default '',
  updated_at        timestamptz not null default now()
);
select enable_tenant_rls('trace_setting');

-- A stored run of the reconciliation for one lot (lot '' = serials without a lot).
create table balance_report (
  id          uuid primary key,
  tenant_id   uuid not null references tenant(id) on delete cascade,
  gtin        text not null,
  lot         text not null default '',
  status      text not null check (status in ('balanced', 'discrepancy')),
  discrepancy int not null default 0,
  allowed     int not null default 0,
  result      jsonb not null,
  alert_id    uuid,
  created_by  text not null default '',
  created_at  timestamptz not null default now()
);
create index balance_report_recent_idx on balance_report (tenant_id, created_at desc);
create index balance_report_lot_idx on balance_report (tenant_id, gtin, lot, created_at desc);
select enable_tenant_rls('balance_report');

-- Pharma lot recall: the scope is a snapshot of where the lot was at the time of the recall.
create table lot_recall (
  id             uuid primary key,
  tenant_id      uuid not null references tenant(id) on delete cascade,
  gtin           text not null,
  lot            text not null,
  title          text not null,
  reason         text not null,
  classification text not null default '' check (classification in ('', 'I', 'II', 'III')),
  status         text not null default 'open' check (status in ('open', 'closed')),
  scope          jsonb not null default '{}',
  created_by     text not null default '',
  created_at     timestamptz not null default now(),
  closed_at      timestamptz
);
create index lot_recall_recent_idx on lot_recall (tenant_id, created_at desc);
select enable_tenant_rls('lot_recall');

create table lot_recall_holder (
  id              uuid primary key,
  tenant_id       uuid not null references tenant(id) on delete cascade,
  recall_id       uuid not null references lot_recall(id) on delete cascade,
  holder_id       text not null,
  gln             text not null default '',
  name            text not null default '',
  partner_id      uuid,
  linked_tenant   uuid,
  packs           int not null default 0,
  sample_serials  text[] not null default '{}',
  channel         text not null default '',
  status          text not null default 'pending' check (status in ('pending', 'sent', 'acknowledged')),
  note            text not null default '',
  sent_at         timestamptz,
  acknowledged_at timestamptz,
  updated_by      text not null default '',
  unique (recall_id, holder_id)
);
create index lot_recall_holder_idx on lot_recall_holder (tenant_id, recall_id);
select enable_tenant_rls('lot_recall_holder');

-- Typed links a Digital Link resolves to. lot '' applies to every lot of the GTIN.
create table resolver_link (
  id         uuid primary key,
  tenant_id  uuid not null references tenant(id) on delete cascade,
  gtin       text not null,
  lot        text not null default '',
  link_type  text not null,
  audiences  text[] not null default '{consumer}',
  mime_type  text not null default 'text/html',
  lang       text not null default '',
  href       text not null,
  title      text not null default '',
  is_default boolean not null default false,
  active     boolean not null default true,
  created_by text not null default '',
  created_at timestamptz not null default now(),
  updated_at timestamptz not null default now(),
  check (audiences <@ array['consumer', 'authority', 'trading_partner']::text[] and cardinality(audiences) > 0)
);
create index resolver_link_gtin_idx on resolver_link (tenant_id, gtin);
select enable_tenant_rls('resolver_link');

-- Signed checkpoints over the audit chain head and a batch of EPCIS events. Append-only.
create table integrity_checkpoint (
  tenant_id    uuid not null, -- no foreign key, like audit_entry: rows are never deleted
  seq          bigint not null,
  created_at   timestamptz not null default now(),
  audit_seq    bigint not null,
  audit_hash   text not null,
  events_from  timestamptz,
  events_to    timestamptz not null,
  event_count  bigint not null default 0,
  event_root   text not null,
  doc_hash     text not null,
  envelope     jsonb not null,
  primary key (tenant_id, seq)
);
select enable_tenant_rls('integrity_checkpoint');

-- +goose StatementBegin
create or replace function checkpoint_immutable() returns trigger language plpgsql as $$
begin
  raise exception 'integrity_checkpoint is append-only';
end
$$;
-- +goose StatementEnd
create trigger checkpoint_no_update before update or delete on integrity_checkpoint for each row execute function checkpoint_immutable();
create trigger checkpoint_no_truncate before truncate on integrity_checkpoint for each statement execute function checkpoint_immutable();

-- +goose Down
drop table integrity_checkpoint, resolver_link, lot_recall_holder, lot_recall, balance_report, trace_setting cascade;
drop function checkpoint_immutable;

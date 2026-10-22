-- +goose Up
create table b2b_document (
  id          uuid primary key,
  tenant_id   uuid not null references tenant(id) on delete cascade,
  type        text not null check (type in ('order','asn','invoice','inventory','return','chargeback')),
  direction   text not null check (direction in ('outbound','inbound')),
  partner_id  uuid references partner(id) on delete set null,
  number      text not null,
  status      text not null default 'draft',
  payload     jsonb not null,
  total       numeric,
  currency    text,
  related_id  uuid,
  delivery    jsonb,
  created_at  timestamptz not null default now(),
  sent_at     timestamptz
);
create index b2b_document_recent_idx on b2b_document (tenant_id, created_at desc);
select enable_tenant_rls('b2b_document');

create table supplier_record (
  id           uuid primary key,
  tenant_id    uuid not null references tenant(id) on delete cascade,
  partner_id   uuid references partner(id) on delete cascade,
  category     text not null default 'certificate' check (category in ('certificate','audit','spec')),
  kind         text not null,
  number       text not null default '',
  issuer       text not null default '',
  valid_from   date,
  valid_to     date,
  result       text not null default '',
  document_id  uuid,
  notes        text not null default '',
  created_at   timestamptz not null default now()
);
create index supplier_record_expiry_idx on supplier_record (tenant_id, valid_to);
select enable_tenant_rls('supplier_record');

create table shared_document (
  id           uuid primary key,
  tenant_id    uuid not null references tenant(id) on delete cascade,
  partner_id   uuid references partner(id) on delete set null,
  direction    text not null default 'internal',
  title        text not null,
  content_type text not null,
  sha256       text not null,
  size         int not null,
  content      bytea not null,
  created_at   timestamptz not null default now()
);
select enable_tenant_rls('shared_document');

-- Node identity (one row per installation, not tenant data).
create table federation_node (
  id          int primary key check (id = 1),
  did         text not null,
  public_key  bytea not null,
  private_key bytea not null,
  created_at  timestamptz not null default now()
);

create table federation_peer (
  id              uuid primary key,
  tenant_id       uuid not null references tenant(id) on delete cascade,
  name            text not null,
  did             text not null,
  url             text not null,
  public_key      bytea not null,
  remote_tenant   text not null default '',
  tls_fingerprint text not null default '',
  status          text not null default 'pending',
  created_at      timestamptz not null default now(),
  unique (tenant_id, did, remote_tenant)
);
select enable_tenant_rls('federation_peer');

create table federation_message (
  id         uuid primary key,
  tenant_id  uuid not null references tenant(id) on delete cascade,
  peer_id    uuid references federation_peer(id) on delete set null,
  direction  text not null,
  msg_id     text not null,
  type       text not null,
  status     text not null,
  detail     jsonb not null default '{}',
  created_at timestamptz not null default now(),
  unique (tenant_id, direction, msg_id)
);
select enable_tenant_rls('federation_message');

-- +goose Down
drop table federation_message, federation_peer, federation_node, shared_document, supplier_record, b2b_document cascade;

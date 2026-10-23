-- +goose Up
-- AS2 (RFC 4130) transport for DSCSA TI/TS exchange.
--
-- as2_station: the tenant's own AS2 identity. The private key is sealed with the tenant key
-- (core/secrets); the certificate is public. The AS2 identifier is unique across the node, because
-- an inbound message is routed to a tenant by its AS2-To header before any tenant is known.
create table as2_station (
  tenant_id   uuid primary key references tenant(id) on delete cascade,
  as2_id      text not null check (as2_id ~ '^[A-Za-z0-9][A-Za-z0-9._:@-]{0,127}$'),
  cert_pem    text not null,
  key_sealed  bytea not null,
  fingerprint text not null,
  not_after   timestamptz not null,
  updated_at  timestamptz not null default now()
);
create unique index as2_station_id_key on as2_station (lower(as2_id));
select enable_tenant_rls('as2_station');

-- partner_as2: the AS2 profile of one partner connection. The partner's certificate is sealed
-- with the tenant key. sign / encrypt are what the connection requires of inbound messages and
-- what is applied to outbound ones. mdn is the receipt mode requested on outbound messages;
-- mdn_url is where async receipts for messages we receive are delivered (default: the partner's
-- AS2 URL).
create table partner_as2 (
  tenant_id   uuid not null references tenant(id) on delete cascade,
  partner_id  uuid not null references partner(id) on delete cascade,
  cert_sealed bytea not null,
  fingerprint text not null,
  not_after   timestamptz not null,
  sign        boolean not null default true,
  encrypt     boolean not null default true,
  mdn         text not null default 'sync' check (mdn in ('sync', 'async')),
  mdn_url     text not null default '',
  updated_at  timestamptz not null default now(),
  primary key (tenant_id, partner_id)
);
select enable_tenant_rls('partner_as2');

-- as2_message: one row per AS2 message sent or received (the message log reads it). The unique
-- key is the Message-ID idempotency guard for inbound messages: a repeated Message-ID from the
-- same sender is answered with the stored receipt and is not processed again. Rejected messages
-- (failed authentication or processing) do not claim their Message-ID, so a sender can resend.
create table as2_message (
  id           uuid primary key,
  tenant_id    uuid not null references tenant(id) on delete cascade,
  partner_id   uuid references partner(id) on delete set null,
  direction    text not null check (direction in ('inbound', 'outbound')),
  message_id   text not null,
  as2_from     text not null,
  as2_to       text not null,
  subject      text not null default '',
  -- inbound: processing, received, rejected. outbound: sent (receipt not requested), mdn_pending
  -- (async receipt awaited), delivered (processed receipt verified), failed.
  status       text not null check (status in ('processing', 'received', 'rejected', 'sent', 'mdn_pending', 'delivered', 'failed')),
  signed       boolean not null default false,
  encrypted    boolean not null default false,
  mdn_mode     text not null default 'none' check (mdn_mode in ('none', 'sync', 'async')),
  mic          text not null default '',
  payload_sha256 text not null default '',
  shipment_id  uuid references shipment(id) on delete set null,
  disposition  text not null default '',
  error        text not null default '',
  -- The receipt we returned for an inbound message (replayed for a duplicate) and, for async,
  -- its delivery state.
  mdn_header   jsonb,
  mdn_body     bytea,
  mdn_url      text not null default '',
  mdn_state    text not null default '' check (mdn_state in ('', 'returned', 'queued', 'delivered', 'failed')),
  created_at   timestamptz not null default now(),
  updated_at   timestamptz not null default now()
);
create unique index as2_message_dedupe on as2_message (tenant_id, direction, as2_from, message_id) where status <> 'rejected';
create index as2_message_created on as2_message (tenant_id, created_at desc);
select enable_tenant_rls('as2_message');

-- TI/TS delivery attempts, shown in the message log; every send, including a retry, counts.
alter table shipment add column send_attempts integer not null default 0;
update shipment set send_attempts = 1 where direction = 'outbound' and status in ('sent', 'send_failed');

-- +goose Down
alter table shipment drop column send_attempts;
drop table as2_message;
drop table partner_as2;
drop table as2_station;

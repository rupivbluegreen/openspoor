-- +goose Up
-- Files exchanged by SFTP integration connector instances (inbound polling and its outcome).
create table sftp_file (
  id          uuid primary key,
  tenant_id   uuid not null references tenant(id) on delete cascade,
  instance_id uuid not null references connector_instance(id) on delete cascade,
  direction   text not null check (direction in ('inbound', 'outbound')),
  name        text not null,
  size        bigint not null default 0,
  sha256      text not null default '',
  format      text not null default '',
  status      text not null check (status in ('processed', 'failed')),
  capture_id  uuid,
  document_id text not null default '',
  moved_to    text not null default '',
  error       text not null default '',
  created_at  timestamptz not null default now()
);
create index sftp_file_recent_idx on sftp_file (tenant_id, instance_id, created_at desc);
select enable_tenant_rls('sftp_file');

-- +goose Down
drop table sftp_file;

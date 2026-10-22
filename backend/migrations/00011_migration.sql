-- +goose Up
create table shadow_run (
  id         uuid primary key,
  tenant_id  uuid not null references tenant(id) on delete cascade,
  label      text not null,
  compared   int not null,
  matched    int not null,
  mismatches jsonb not null default '[]',
  created_at timestamptz not null default now()
);
select enable_tenant_rls('shadow_run');

-- +goose Down
drop table shadow_run;

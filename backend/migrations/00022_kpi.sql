-- +goose Up
-- B2B transaction types come from the registry in pkg/edi (60 entries and growing), so the
-- two hard-coded type lists are replaced by a shape check; the registry validates the value.
alter table b2b_document drop constraint if exists b2b_document_type_check;
alter table b2b_document add constraint b2b_document_type_key check (type ~ '^[a-z][a-z0-9_]*$');
alter table b2b_mapping drop constraint if exists b2b_mapping_doc_type_check;
alter table b2b_mapping add constraint b2b_mapping_doc_type_key check (doc_type ~ '^[a-z][a-z0-9_]*$');

-- KPI library: a tenant's own goal per KPI (the KPI definitions themselves live in code).
create table kpi_goal (
  tenant_id  uuid not null references tenant(id) on delete cascade,
  kpi_id     text not null check (kpi_id ~ '^[a-z][a-z0-9_]*$'),
  target     numeric not null,
  note       text not null default '',
  updated_by text not null default '',
  updated_at timestamptz not null default now(),
  primary key (tenant_id, kpi_id)
);
select enable_tenant_rls('kpi_goal');

-- +goose Down
drop table kpi_goal;
alter table b2b_mapping drop constraint b2b_mapping_doc_type_key;
alter table b2b_mapping add constraint b2b_mapping_doc_type_check
  check (doc_type in ('order','asn','invoice','inventory','return','chargeback','epcis')) not valid;
alter table b2b_document drop constraint b2b_document_type_key;
alter table b2b_document add constraint b2b_document_type_check
  check (type in ('order','asn','invoice','inventory','return','chargeback')) not valid;

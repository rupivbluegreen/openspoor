-- +goose Up
-- Measured agent scores (the autonomy ladder consults them) and extracted document text with
-- PostgreSQL full-text search.

-- One row per evaluation run of one governed agent against the synthetic golden dataset. The score is
-- a measured result on that small synthetic dataset, not an accuracy claim about real data.
create table agent_eval_score (
  id            uuid primary key,
  tenant_id     uuid not null references tenant(id) on delete cascade,
  agent         text not null,
  score         numeric(5,4) not null check (score between 0 and 1),
  precision     numeric(5,4) not null check (precision between 0 and 1),
  recall        numeric(5,4) not null check (recall between 0 and 1),
  cases         int not null check (cases >= 0),
  passed_cases  int not null check (passed_cases >= 0),
  pass_score    numeric(5,4) not null check (pass_score between 0 and 1),
  dataset       text not null,
  synthetic     boolean not null default true,
  run_by        text not null default '',
  run_at        timestamptz not null default now()
);
create index agent_eval_score_latest_idx on agent_eval_score (tenant_id, agent, run_at desc);
select enable_tenant_rls('agent_eval_score');

-- The tenant's minimum score for raising a tool above "approve" (one row per tenant).
create table agent_eval_policy (
  tenant_id  uuid primary key references tenant(id) on delete cascade,
  min_score  numeric(5,4) not null check (min_score between 0 and 1),
  updated_by text not null default '',
  updated_at timestamptz not null default now()
);
select enable_tenant_rls('agent_eval_policy');

-- Uploaded documents: only the extracted text is kept (no original file, no OCR).
create table document (
  id          uuid primary key,
  tenant_id   uuid not null references tenant(id) on delete cascade,
  name        text not null,
  format      text not null check (format in ('pdf','csv','xml','html','text')),
  media_type  text not null default '',
  size_bytes  int not null,
  sha256      text not null,
  pages       int not null default 0,
  chars       int not null default 0,
  truncated   boolean not null default false,
  warnings    text[] not null default '{}',
  created_by  text not null default '',
  created_at  timestamptz not null default now(),
  unique (tenant_id, sha256)
);
select enable_tenant_rls('document');

create table document_chunk (
  id          uuid primary key,
  tenant_id   uuid not null references tenant(id) on delete cascade,
  document_id uuid not null references document(id) on delete cascade,
  seq         int not null,
  body        text not null,
  -- 'simple' keeps the search language-neutral (English, Dutch, Arabic documents share one index).
  tsv         tsvector generated always as (to_tsvector('simple', body)) stored,
  unique (document_id, seq)
);
create index document_chunk_tsv_idx on document_chunk using gin (tsv);
select enable_tenant_rls('document_chunk');

-- +goose Down
drop table document_chunk, document, agent_eval_policy, agent_eval_score cascade;

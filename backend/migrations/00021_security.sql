-- +goose Up
-- Failed-login counters for brute-force protection (OWASP ASVS V2.2). They are consulted before a
-- user is known, so the table is deliberately not tenant-scoped and has no row-level security; it
-- holds no personal data: keys are keyed hashes ("a:" account = tenant + e-mail, "i:" client
-- address), never the e-mail address, the address or any password. Rows are short-lived and
-- deleted on a successful login or when stale. A role other than the table owner needs
-- select, insert, update and delete on it.
create table login_attempt (
  key            text primary key,
  failures       integer not null default 0,
  last_failed_at timestamptz not null default now(),
  locked_until   timestamptz
);
create index login_attempt_last_failed_idx on login_attempt (last_failed_at);

-- +goose Down
drop table login_attempt;

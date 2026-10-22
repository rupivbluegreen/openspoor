#!/usr/bin/env bash
# Starts a throwaway PostgreSQL 16 for tests without Docker.
# Prints the env vars to export. Usage: eval "$(scripts/testdb.sh)"
set -euo pipefail
PGBIN=${PGBIN:-$(ls -d /usr/lib/postgresql/16/bin 2>/dev/null || dirname "$(command -v pg_ctl)")}
DIR=${PGDATA_DIR:-/tmp/openspoor-pg}
PORT=${PGPORT:-54329}
if [ ! -f "$DIR/PG_VERSION" ]; then
  mkdir -p "$DIR"
  chown -R "${PGUSER_OS:-postgres}" "$DIR" 2>/dev/null || true
  run() { if [ "$(id -u)" = 0 ]; then su postgres -c "$*"; else bash -c "$*"; fi; }
  run "$PGBIN/initdb -D $DIR -U postgres --auth=trust >/dev/null"
  cat >> "$DIR/postgresql.conf" <<CONF
port = $PORT
listen_addresses = '127.0.0.1'
unix_socket_directories = '$DIR'
max_connections = 300
shared_buffers = 512MB
synchronous_commit = on
fsync = on
CONF
fi
run() { if [ "$(id -u)" = 0 ]; then su postgres -c "$*"; else bash -c "$*"; fi; }
if ! run "$PGBIN/pg_ctl -D $DIR status" >/dev/null 2>&1; then
  run "$PGBIN/pg_ctl -D $DIR -l $DIR/log -w start" >/dev/null
fi
psql "postgres://postgres@127.0.0.1:$PORT/postgres" -qtAc "do \$\$ begin if not exists (select from pg_roles where rolname='openspoor') then create role openspoor login password 'openspoor' createdb; end if; end \$\$;" >/dev/null
echo "export OPENSPOOR_TEST_ADMIN_URL=postgres://postgres@127.0.0.1:$PORT/postgres?sslmode=disable"
echo "export OPENSPOOR_TEST_DATABASE_URL=postgres://openspoor:openspoor@127.0.0.1:$PORT/postgres?sslmode=disable"

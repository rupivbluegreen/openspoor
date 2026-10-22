#!/usr/bin/env bash
# Reproducible load test: fresh database, demo seed (10k packs), api+worker, k6 capture + VRS.
# Needs: Go, k6 (K6=path/to/k6), PostgreSQL 16 binaries (or set OPENSPOOR_TEST_ADMIN_URL).
# Results are written to bench-results/k6-*.json.
set -euo pipefail
cd "$(dirname "$0")/.."
K6=${K6:-k6}
eval "$(scripts/testdb.sh)"
DB=openspoor_load
psql "$OPENSPOOR_TEST_ADMIN_URL" -qc "drop database if exists $DB with (force)" -c "create database $DB owner openspoor" >/dev/null
export OPENSPOOR_DATABASE_URL="${OPENSPOOR_TEST_DATABASE_URL%/*}/$DB?sslmode=disable"
export OPENSPOOR_DEV_AUTH=true OPENSPOOR_HTTP_ADDR=127.0.0.1:18080 OPENSPOOR_LOG_LEVEL=warn OPENSPOOR_WORKER_CONCURRENCY=4
(cd backend && go build -o bin/openspoor ./cmd/openspoor)
backend/bin/openspoor migrate >/dev/null
backend/bin/openspoor seed --packs "${PACKS:-10000}" --password load-test-password >/dev/null
backend/bin/openspoor all >/tmp/openspoor-load.log 2>&1 &
PID=$!
trap 'kill $PID 2>/dev/null || true' EXIT
for i in $(seq 1 50); do curl -sf http://127.0.0.1:18080/readyz >/dev/null && break; sleep 0.2; done
BASE=http://127.0.0.1:18080
TOKEN=$(curl -sf $BASE/api/v1/auth/login -H 'Content-Type: application/json' -d '{"tenant":"nordwind","email":"admin@nordwind.example","password":"load-test-password"}' | python3 -c 'import sys,json;print(json.load(sys.stdin)["token"])')
KEY=$(curl -sf $BASE/api/v1/api-keys -H "Authorization: Bearer $TOKEN" -H 'Content-Type: application/json' -d '{"name":"k6","roles":["Integration","Serialization"]}' | python3 -c 'import sys,json;print(json.load(sys.stdin)["secret"])')
GTIN=$(curl -sf "$BASE/api/v1/products" -H "Authorization: Bearer $TOKEN" | python3 -c 'import sys,json;print(sorted(p["gtin"] for p in json.load(sys.stdin)["items"])[0])')
mkdir -p bench-results
BASE_URL=$BASE API_KEY=$KEY VUS=${VUS:-4} $K6 run --quiet --summary-export bench-results/k6-capture-vus${VUS:-4}.json deploy/k6/capture.js || echo "capture thresholds crossed"
BASE_URL=$BASE API_KEY=$KEY TOKEN=$TOKEN GTIN=$GTIN $K6 run --quiet --summary-export bench-results/k6-vrs.json deploy/k6/vrs.js || echo "vrs thresholds crossed"

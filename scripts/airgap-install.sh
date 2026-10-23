#!/usr/bin/env bash
# Installs Open Spoor from an offline bundle (see airgap-bundle.sh). Needs only Docker with the
# compose plugin on the target host; nothing is downloaded.
#
#   ./install.sh            load images, start the stack in hub mode, apply migrations
set -euo pipefail
cd "$(dirname "$0")"
[ -f openspoor.env ] || { echo "openspoor.env missing" >&2; exit 1; }
set -a; . ./openspoor.env; set +a
for v in OPENSPOOR_JWT_SECRET OPENSPOOR_MASTER_KEY; do
  [ -n "${!v:-}" ] || { echo "set $v in openspoor.env (at least 32 random bytes, e.g. openssl rand -hex 32)" >&2; exit 1; }
done
gunzip -c images/images.tar.gz | docker load
docker compose -f docker-compose.yml --profile app up -d --no-build
echo "Open Spoor is starting in $OPENSPOOR_MODE mode on port 8080."
echo "Create the first tenant: docker compose -f docker-compose.yml --profile app exec openspoor /openspoor bootstrap --slug <slug> --name <name> --email <email> --password <password>"

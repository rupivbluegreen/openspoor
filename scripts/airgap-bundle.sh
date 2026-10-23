#!/usr/bin/env bash
# Builds an offline installer for air-gapped sites (national hub mode): every container image the
# stack needs, the compose file, the Helm chart and the install script, in one tarball.
# Run on a connected machine with Docker; copy the tarball across the air gap; run airgap-install.sh there.
#
#   scripts/airgap-bundle.sh [version]      -> dist/openspoor-airgap-<version>.tar.gz (+ .sha256)
set -euo pipefail
cd "$(dirname "$0")/.."
VERSION=${1:-$(git describe --tags --always 2>/dev/null || echo dev)}
OUT=dist/openspoor-airgap-$VERSION
rm -rf "$OUT" && mkdir -p "$OUT/images"

docker build -f deploy/Dockerfile --build-arg VERSION="$VERSION" -t "openspoor/openspoor:$VERSION" .
# Third-party images, read from the compose file so the bundle never drifts from it.
mapfile -t IMAGES < <(grep -E '^\s+image:' deploy/docker-compose.yml | awk '{print $2}')
for img in "${IMAGES[@]}"; do docker pull "$img"; done
docker save "openspoor/openspoor:$VERSION" "${IMAGES[@]}" | gzip -1 > "$OUT/images/images.tar.gz"

cp deploy/docker-compose.yml "$OUT/"
cp -r deploy/postgres deploy/keycloak "$OUT/"
cp -r deploy/helm "$OUT/"
cp scripts/airgap-install.sh "$OUT/install.sh"
cat > "$OUT/openspoor.env" <<ENV
# Offline (hub) defaults. Set real secrets before the first start.
OPENSPOOR_IMAGE=openspoor/openspoor:$VERSION
OPENSPOOR_MODE=hub
OPENSPOOR_DEV_AUTH=false
OPENSPOOR_JWT_SECRET=
OPENSPOOR_MASTER_KEY=
ENV
printf '%s\n' "openspoor/openspoor:$VERSION" "${IMAGES[@]}" > "$OUT/images/IMAGES"

tar -C dist -czf "$OUT.tar.gz" "$(basename "$OUT")"
sha256sum "$OUT.tar.gz" > "$OUT.tar.gz.sha256"
echo "bundle: $OUT.tar.gz"

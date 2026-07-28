#!/usr/bin/env bash
set -euo pipefail

CONTROLLER_DIR=$(CDPATH= cd -- "$(dirname -- "$0")/.." && pwd)
BUILD_DIR=$(mktemp -d)
trap 'rm -rf "$BUILD_DIR"' EXIT

echo "==> Verifying pinned IP geolocation databases"
"$CONTROLLER_DIR/scripts/fetch-ip2region.sh" "$BUILD_DIR/geoip"

echo "==> Testing Controller"
OBOARD_TEST_GEOIP_DIR="$BUILD_DIR/geoip" go -C "$CONTROLLER_DIR" test ./...

echo "==> Building Web UI"
(
  cd "$CONTROLLER_DIR/web"
  npm ci --include=dev
  npm run build -- --outDir "$BUILD_DIR/web" --emptyOutDir
)

echo "==> Building current-platform binaries"
go -C "$CONTROLLER_DIR" build -o "$BUILD_DIR/oboard-controller" ./cmd/controller
go -C "$CONTROLLER_DIR" build -o "$BUILD_DIR/oboard-controller-updater" ./cmd/controller-updater

echo "==> Release verification passed"

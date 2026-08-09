#!/usr/bin/env bash
set -euo pipefail

CONTROLLER_DIR=$(CDPATH= cd -- "$(dirname -- "$0")/.." && pwd)
BUILD_DIR=$(mktemp -d)
trap 'rm -rf "$BUILD_DIR"' EXIT
export GOWORK=off

echo "==> Verifying shell script syntax"
for script in "$CONTROLLER_DIR"/scripts/*.sh; do
  case $(head -n 1 "$script") in
    '#!/bin/sh'|'#!/usr/bin/env sh') sh -n "$script" ;;
    '#!/usr/bin/env bash'|'#!/bin/bash') bash -n "$script" ;;
    *) echo "unsupported shell shebang: $script" >&2; exit 1 ;;
  esac
done

echo "==> Verifying Go module metadata"
go -C "$CONTROLLER_DIR" mod tidy -diff
go -C "$CONTROLLER_DIR" mod verify

echo "==> Verifying pinned IP geolocation databases"
"$CONTROLLER_DIR/scripts/fetch-ip2region.sh" "$BUILD_DIR/geoip"

echo "==> Generating client skill packs"
"$CONTROLLER_DIR/scripts/sync-client-skills.sh" write

echo "==> Testing Controller"
OBOARD_TEST_GEOIP_DIR="$BUILD_DIR/geoip" go -C "$CONTROLLER_DIR" test ./...

echo "==> Building Web UI"
(
  cd "$CONTROLLER_DIR/web"
  npm ci --include=dev
  npm test
  npm run build -- --outDir "$BUILD_DIR/web" --emptyOutDir
)

echo "==> Building current-platform binaries"
go -C "$CONTROLLER_DIR" build -o "$BUILD_DIR/oboard-controller" ./cmd/controller
go -C "$CONTROLLER_DIR" build -o "$BUILD_DIR/oboard-controller-updater" ./cmd/controller-updater
go -C "$CONTROLLER_DIR" build -o "$BUILD_DIR/oboard-ai-worker" ./cmd/ai-worker

echo "==> Release verification passed"

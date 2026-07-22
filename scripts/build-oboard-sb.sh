#!/usr/bin/env sh
set -eu

CONTROLLER_DIR=$(CDPATH= cd -- "$(dirname -- "$0")/.." && pwd)
WORKSPACE_DIR=$(CDPATH= cd -- "$CONTROLLER_DIR/.." && pwd)
KERNEL_DIR="$WORKSPACE_DIR/oboard-agent/kernel/oboard-sb"
OUT_DIR="${OUT_DIR:-$WORKSPACE_DIR/bin}"
GOOS_VALUE="${GOOS:-$(go env GOOS)}"
GOARCH_VALUE="${GOARCH:-$(go env GOARCH)}"
CGO_ENABLED_VALUE="${CGO_ENABLED:-0}"
VERSION_VALUE="${VERSION:-$(tr -d '[:space:]' < "$CONTROLLER_DIR/VERSION")}"
COMMIT_VALUE="${COMMIT:-$(git -C "$CONTROLLER_DIR" rev-parse --short HEAD 2>/dev/null || git -C "$WORKSPACE_DIR" rev-parse --short HEAD 2>/dev/null || echo unknown)}"
DATE_VALUE="${DATE:-$(date -u +%Y-%m-%dT%H:%M:%SZ)}"
BUILD_VALUE="${BUILD:-${BUILD_NUMBER:-$(date -u +%Y%m%d%H%M%S)}}"
TAGS_VALUE="${OBOARD_SB_TAGS:-with_utls}"
LDFLAGS="-s -w -X github.com/OboardProject/oboard-agent/kernel/oboard-sb/internal/version.Version=$VERSION_VALUE -X github.com/OboardProject/oboard-agent/kernel/oboard-sb/internal/version.Build=$BUILD_VALUE -X github.com/OboardProject/oboard-agent/kernel/oboard-sb/internal/version.Commit=$COMMIT_VALUE -X github.com/OboardProject/oboard-agent/kernel/oboard-sb/internal/version.Date=$DATE_VALUE"

mkdir -p "$OUT_DIR"

cd "$KERNEL_DIR"
CGO_ENABLED="$CGO_ENABLED_VALUE" GOOS="$GOOS_VALUE" GOARCH="$GOARCH_VALUE" \
  go build -trimpath -tags "$TAGS_VALUE" -ldflags "$LDFLAGS" -o "$OUT_DIR/oboard-sb-$GOOS_VALUE-$GOARCH_VALUE" ./cmd/oboard-sb

echo "$OUT_DIR/oboard-sb-$GOOS_VALUE-$GOARCH_VALUE"

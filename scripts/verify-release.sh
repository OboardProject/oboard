#!/usr/bin/env bash
set -euo pipefail

CONTROLLER_DIR=$(CDPATH= cd -- "$(dirname -- "$0")/.." && pwd)
AGENT_DIR=${OBOARD_AGENT_DIR:-}
if [ -z "$AGENT_DIR" ]; then
  if [ -f "$CONTROLLER_DIR/oboard-agent/go.mod" ]; then
    AGENT_DIR="$CONTROLLER_DIR/oboard-agent"
  else
    AGENT_DIR="$CONTROLLER_DIR/../oboard-agent"
  fi
fi
if [ ! -f "$AGENT_DIR/go.mod" ]; then
  echo "oboard-agent source not found. Set OBOARD_AGENT_DIR or check out OboardProject/oboard-agent beside/inside this repository." >&2
  exit 1
fi

BUILD_DIR=$(mktemp -d)
trap 'rm -rf "$BUILD_DIR"' EXIT

echo "==> Testing Controller"
go -C "$CONTROLLER_DIR" test ./...

echo "==> Testing Agent"
go -C "$AGENT_DIR" test ./...

echo "==> Testing oboard-sb"
go -C "$AGENT_DIR/kernel/oboard-sb" test ./...

echo "==> Building Web UI"
(
  cd "$CONTROLLER_DIR/web"
  npm ci
  npm run build
)

echo "==> Building current-platform binaries"
go -C "$CONTROLLER_DIR" build -o "$BUILD_DIR/oboard-controller" ./cmd/controller
go -C "$AGENT_DIR" build -o "$BUILD_DIR/oboard-agent" ./cmd/agent
go -C "$AGENT_DIR/kernel/oboard-sb" build -o "$BUILD_DIR/oboard-sb" ./cmd/oboard-sb

echo "==> Release verification passed"

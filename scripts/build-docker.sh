#!/usr/bin/env bash
set -euo pipefail

ROOT=$(CDPATH= cd -- "$(dirname -- "$0")/.." && pwd)
VERSION_VALUE=${VERSION:-$(tr -d '[:space:]' < "$ROOT/VERSION")}
BUILD_VALUE=${BUILD:-${BUILD_NUMBER:-$(date -u +%Y%m%d%H%M%S)}}
COMMIT_VALUE=${COMMIT:-$(git -C "$ROOT" rev-parse --short HEAD 2>/dev/null || echo unknown)}
DATE_VALUE=${DATE:-$(date -u +%Y-%m-%dT%H:%M:%SZ)}
IMAGE=${OBOARD_IMAGE:-ghcr.io/imnebula/oboard}
TAG=${OBOARD_TAG:-}

if [ -z "$TAG" ]; then
  case "$VERSION_VALUE" in
    *dev*) TAG=dev ;;
    *) TAG=${VERSION_VALUE#v} ;;
  esac
fi

if [ -z "${OBOARD_RELEASE_SIGNING_KEY:-}" ] && [[ "$VERSION_VALUE" == *dev* ]]; then
  key_file="$ROOT/.tmp/oboard-dev-release-signing-key"
  mkdir -p "$(dirname "$key_file")"
  if [ ! -s "$key_file" ]; then
    python3 - <<'PY' > "$key_file"
import base64, os
print(base64.b64encode(os.urandom(32)).decode())
PY
    chmod 0600 "$key_file"
  fi
  export OBOARD_RELEASE_SIGNING_KEY
  OBOARD_RELEASE_SIGNING_KEY=$(cat "$key_file")
fi

export VERSION="$VERSION_VALUE" BUILD="$BUILD_VALUE" COMMIT="$COMMIT_VALUE" DATE="$DATE_VALUE"
"$ROOT/scripts/build-release.sh"
"$ROOT/scripts/prepare-docker-downloads.sh"

AGENT_DIR=${OBOARD_AGENT_DIR:-}
if [ -z "$AGENT_DIR" ]; then
  if [ -f "$ROOT/oboard-agent/go.mod" ]; then AGENT_DIR="$ROOT/oboard-agent"; else AGENT_DIR="$ROOT/../oboard-agent"; fi
fi
RELEASE_PUBLIC_KEY=$(OBOARD_RELEASE_SIGNING_KEY="$OBOARD_RELEASE_SIGNING_KEY" go -C "$AGENT_DIR" run ./scripts/print_release_public_key.go)

docker build \
  --file "$ROOT/deploy/docker/Dockerfile.controller" \
  --tag "$IMAGE:$TAG" \
  --build-arg VERSION="$VERSION_VALUE" \
  --build-arg BUILD="$BUILD_VALUE" \
  --build-arg COMMIT="$COMMIT_VALUE" \
  --build-arg BUILD_DATE="$DATE_VALUE" \
  --build-arg RELEASE_PUBLIC_KEY="$RELEASE_PUBLIC_KEY" \
  "$ROOT"

echo "Docker image built: $IMAGE:$TAG"

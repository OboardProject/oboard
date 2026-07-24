#!/usr/bin/env bash
set -euo pipefail

ROOT=$(CDPATH= cd -- "$(dirname -- "$0")/.." && pwd)
WORKSPACE=$(CDPATH= cd -- "$ROOT/.." && pwd)
OUT_DIR=${OUT_DIR:-$WORKSPACE/dist/controller/release}
DOCKER_DOWNLOADS_DIR=${OBOARD_DOCKER_DOWNLOADS_DIR:-$WORKSPACE/dist/controller/docker-downloads}
VERSION_VALUE=${VERSION:-$(tr -d '[:space:]' < "$ROOT/VERSION")}
BUILD_VALUE=${BUILD:-${BUILD_NUMBER:-$(date -u +%Y%m%d%H%M%S)}}
COMMIT_VALUE=${COMMIT:-$(git -C "$ROOT" rev-parse --short HEAD 2>/dev/null || echo unknown)}
DATE_VALUE=${DATE:-$(date -u +%Y-%m-%dT%H:%M:%SZ)}
IMAGE=${OBOARD_IMAGE:-ghcr.io/oboardproject/oboard}
TAG=${OBOARD_TAG:-}

if [ -z "$TAG" ]; then
  case "$VERSION_VALUE" in
    *dev*) TAG=dev ;;
    *) TAG=${VERSION_VALUE#v} ;;
  esac
fi

export VERSION="$VERSION_VALUE" BUILD="$BUILD_VALUE" COMMIT="$COMMIT_VALUE" DATE="$DATE_VALUE" OUT_DIR
"$ROOT/scripts/build-release.sh"
OBOARD_AGENT_RELEASE_DIR="$OUT_DIR/agent-release" OBOARD_DOCKER_DOWNLOADS_DIR="$DOCKER_DOWNLOADS_DIR" "$ROOT/scripts/prepare-docker-downloads.sh"

read -r AGENT_VERSION AGENT_BUILD AGENT_COMMIT AGENT_DATE < <(python3 - "$OUT_DIR/agent-release/release-metadata.json" <<'PY'
import json, sys
m = json.load(open(sys.argv[1]))
print(m["version"], m["build"], m["commit"], m["date"])
PY
)
RELEASE_PUBLIC_KEY=${OBOARD_RELEASE_PUBLIC_KEY:?OBOARD_RELEASE_PUBLIC_KEY must contain the Agent release Ed25519 public key}

docker buildx build --load \
  --file "$ROOT/deploy/docker/Dockerfile.controller" \
  --build-context agent-downloads="$DOCKER_DOWNLOADS_DIR" \
  --tag "$IMAGE:$TAG" \
  --build-arg VERSION="$VERSION_VALUE" \
  --build-arg BUILD="$BUILD_VALUE" \
  --build-arg COMMIT="$COMMIT_VALUE" \
  --build-arg BUILD_DATE="$DATE_VALUE" \
  --build-arg RELEASE_PUBLIC_KEY="$RELEASE_PUBLIC_KEY" \
  --build-arg AGENT_VERSION="$AGENT_VERSION" \
  --build-arg AGENT_BUILD="$AGENT_BUILD" \
  --build-arg AGENT_COMMIT="$AGENT_COMMIT" \
  --build-arg AGENT_DATE="$AGENT_DATE" \
  --build-arg KERNEL_VERSION="$AGENT_VERSION" \
  --build-arg KERNEL_BUILD="$AGENT_BUILD" \
  "$ROOT"

echo "Docker image built: $IMAGE:$TAG"

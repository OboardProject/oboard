#!/bin/sh
set -eu

ROOT=$(CDPATH= cd -- "$(dirname -- "$0")/.." && pwd)
WORKSPACE=$(CDPATH= cd -- "$ROOT/.." && pwd)
SOURCE=${OBOARD_AGENT_RELEASE_DIR:-$WORKSPACE/dist/controller/release/agent-release}
TARGET=${OBOARD_DOCKER_DOWNLOADS_DIR:-$WORKSPACE/dist/controller/docker-downloads}

rm -rf "$TARGET"
mkdir -p "$TARGET"
for file in \
  oboard-agent-linux-amd64 \
  oboard-agent-linux-arm64 \
  oboard-sb-linux-amd64 \
  oboard-sb-linux-arm64 \
  release-manifest.json \
  release-manifest.json.sig
do
  if [ ! -f "$SOURCE/$file" ]; then
    echo "Docker image requires $SOURCE/$file" >&2
    exit 1
  fi
  cp "$SOURCE/$file" "$TARGET/$file"
done

echo "Docker download payload prepared in $TARGET"

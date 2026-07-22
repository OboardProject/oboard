#!/usr/bin/env bash
set -euo pipefail

CONTROLLER_DIR=$(CDPATH= cd -- "$(dirname -- "$0")/.." && pwd)
WORKSPACE_DIR=$(CDPATH= cd -- "$CONTROLLER_DIR/.." && pwd)
AGENT_DIR=${OBOARD_AGENT_DIR:-}
if [ -z "$AGENT_DIR" ]; then
  if [ -f "$CONTROLLER_DIR/oboard-agent/go.mod" ]; then
    AGENT_DIR="$CONTROLLER_DIR/oboard-agent"
  else
    AGENT_DIR="$WORKSPACE_DIR/oboard-agent"
  fi
fi
if [ ! -f "$AGENT_DIR/go.mod" ]; then
  echo "oboard-agent source not found. Set OBOARD_AGENT_DIR or check out OboardProject/oboard-agent beside/inside this repository." >&2
  exit 1
fi
VERSION_VALUE=${VERSION:-$(tr -d '[:space:]' < "$CONTROLLER_DIR/VERSION")}
COMMIT_VALUE=${COMMIT:-$(git -C "$CONTROLLER_DIR" rev-parse --short HEAD 2>/dev/null || git -C "$WORKSPACE_DIR" rev-parse --short HEAD 2>/dev/null || echo unknown)}
DATE_VALUE=${DATE:-$(date -u +%Y-%m-%dT%H:%M:%SZ)}
BUILD_VALUE=${BUILD:-${BUILD_NUMBER:-$(date -u +%Y%m%d%H%M%S)}}
OUT_DIR=${OUT_DIR:-$CONTROLLER_DIR/dist/release}
PLATFORMS=${OBOARD_PLATFORMS:-"linux/amd64 linux/arm64"}

RELEASE_PUBLIC_KEY=""
if [ -n "${OBOARD_RELEASE_SIGNING_KEY:-}" ]; then
  RELEASE_PUBLIC_KEY=$(go -C "$AGENT_DIR" run ./scripts/print_release_public_key.go)
fi
CONTROLLER_LDFLAGS="-s -w -X github.com/OboardProject/oboard/internal/version.Version=$VERSION_VALUE -X github.com/OboardProject/oboard/internal/version.Build=$BUILD_VALUE -X github.com/OboardProject/oboard/internal/version.Commit=$COMMIT_VALUE -X github.com/OboardProject/oboard/internal/version.Date=$DATE_VALUE -X github.com/OboardProject/oboard/internal/version.ReleasePublicKey=$RELEASE_PUBLIC_KEY"

rm -rf "$OUT_DIR"
mkdir -p "$OUT_DIR"

echo "==> Building signed Agent artifacts"
OUT_DIR="$OUT_DIR/agent-release" OBOARD_PLATFORMS="$PLATFORMS" VERSION="$VERSION_VALUE" BUILD="$BUILD_VALUE" COMMIT="$COMMIT_VALUE" DATE="$DATE_VALUE" "$AGENT_DIR/scripts/build-release.sh"

echo "==> Building web assets"
cd "$CONTROLLER_DIR/web"
if [ ! -d node_modules ]; then
  npm ci
fi
npm run build

package_controller() {
  local os=$1 arch=$2 source=$3
  local stage
  stage=$(mktemp -d)
  mkdir -p "$stage/bin" "$stage/deploy/systemd" "$stage/deploy/openrc" "$stage/docs"
  cp "$source" "$stage/bin/oboard-controller"
  cp "$CONTROLLER_DIR/README.md" "$stage/README.md"
  cp "$CONTROLLER_DIR/LICENSE" "$stage/LICENSE"
  cp "$CONTROLLER_DIR/VERSION" "$stage/VERSION"
  # Optional maintainer note lives in workspace docs/ (not inside the product repo).
  if [ -f "$CONTROLLER_DIR/../docs/RELEASE_GUIDE.md" ]; then
    cp "$CONTROLLER_DIR/../docs/RELEASE_GUIDE.md" "$stage/docs/RELEASE_GUIDE.md"
  fi

  mkdir -p "$stage/web"
  cp -R "$CONTROLLER_DIR/web/dist" "$stage/web/dist"
  mkdir -p "$stage/downloads"
  for f in "release-manifest.json" "release-manifest.json.sig"; do
    if [ -f "$OUT_DIR/agent-release/$f" ]; then cp "$OUT_DIR/agent-release/$f" "$stage/downloads/$f"; fi
  done
  for f in "$OUT_DIR"/agent-release/oboard-agent-* "$OUT_DIR"/agent-release/oboard-sb-*; do
    if [ -f "$f" ]; then cp "$f" "$stage/downloads/"; fi
  done
  cp "$CONTROLLER_DIR/deploy/systemd/oboard-controller.service" "$stage/deploy/systemd/"
  cp "$CONTROLLER_DIR/deploy/openrc/oboard-controller" "$stage/deploy/openrc/"
  cp "$CONTROLLER_DIR/deploy/controller.env.example" "$stage/deploy/"
  mkdir -p "$stage/deploy/docker"
  cp "$CONTROLLER_DIR/deploy/docker-compose.yml" "$stage/deploy/"
  cp "$CONTROLLER_DIR/deploy/docker/Dockerfile.controller" "$stage/deploy/docker/"
  cp "$CONTROLLER_DIR/deploy/docker/entrypoint.sh" "$stage/deploy/docker/"
  mkdir -p "$stage/scripts"
  cp "$CONTROLLER_DIR/scripts/install-docker.sh" "$stage/scripts/"
  cp "$CONTROLLER_DIR/scripts/update-docker.sh" "$stage/scripts/"

  local archive="$OUT_DIR/oboard_controller_${VERSION_VALUE}_${os}_${arch}.tar.gz"
  if tar --help 2>&1 | grep -q -- '--no-xattrs'; then
    COPYFILE_DISABLE=1 tar --no-xattrs -C "$stage" -czf "$archive" .
  else
    COPYFILE_DISABLE=1 tar -C "$stage" -czf "$archive" .
  fi
  rm -rf "$stage"
  echo "$archive"
}

for platform in $PLATFORMS; do
  os=${platform%/*}
  arch=${platform#*/}

  echo "==> Building controller $os/$arch"
  mkdir -p "$OUT_DIR/bin/$os-$arch"
  CGO_ENABLED=0 GOOS="$os" GOARCH="$arch" go -C "$CONTROLLER_DIR" build -trimpath -ldflags "$CONTROLLER_LDFLAGS" -o "$OUT_DIR/bin/$os-$arch/oboard-controller" ./cmd/controller
  package_controller "$os" "$arch" "$OUT_DIR/bin/$os-$arch/oboard-controller" >/dev/null
done

(
  cd "$OUT_DIR"
  find . -maxdepth 1 -name '*.tar.gz' -print0 | sort -z | xargs -0 shasum -a 256 | sed 's#  \./#  #'
) > "$OUT_DIR/sha256sums.txt"
echo "==> Release artifacts written to $OUT_DIR"

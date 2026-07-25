#!/usr/bin/env bash
set -euo pipefail

CONTROLLER_DIR=$(CDPATH= cd -- "$(dirname -- "$0")/.." && pwd)
WORKSPACE_DIR=$(CDPATH= cd -- "$CONTROLLER_DIR/.." && pwd)
VERSION_VALUE=${VERSION:-$(tr -d '[:space:]' < "$CONTROLLER_DIR/VERSION")}
COMMIT_VALUE=${COMMIT:-$(git -C "$CONTROLLER_DIR" rev-parse --short HEAD 2>/dev/null || git -C "$WORKSPACE_DIR" rev-parse --short HEAD 2>/dev/null || echo unknown)}
DATE_VALUE=${DATE:-$(date -u +%Y-%m-%dT%H:%M:%SZ)}
BUILD_VALUE=${BUILD:-${BUILD_NUMBER:-$(date -u +%Y%m%d%H%M%S)}}
OUT_DIR=${OUT_DIR:-$WORKSPACE_DIR/dist/controller/release}
WEB_OUT_DIR=${WEB_OUT_DIR:-$WORKSPACE_DIR/dist/controller/web}
PLATFORMS=${OBOARD_PLATFORMS:-"linux/amd64 linux/arm64"}
if [ -n "${OBOARD_RELEASE_CHANNEL:-}" ]; then
  RELEASE_CHANNEL=$OBOARD_RELEASE_CHANNEL
elif [[ "$VERSION_VALUE" == *dev* ]]; then
  RELEASE_CHANNEL=dev
elif [[ "$VERSION_VALUE" == *-* ]]; then
  RELEASE_CHANNEL=prerelease
else
  RELEASE_CHANNEL=stable
fi
if [ -n "${OBOARD_ARTIFACT_VERSION:-}" ]; then
  ARTIFACT_VERSION=$OBOARD_ARTIFACT_VERSION
elif [ "$RELEASE_CHANNEL" = dev ]; then
  ARTIFACT_VERSION=dev
else
  ARTIFACT_VERSION=$VERSION_VALUE
fi

rm -rf "$OUT_DIR"
mkdir -p "$OUT_DIR"

create_tar_archive() {
  local stage=$1 archive=$2
  shift 2
  if tar --help 2>&1 | grep -q -- '--no-xattrs'; then
    COPYFILE_DISABLE=1 tar --no-xattrs -C "$stage" -czf "$archive" "$@"
  else
    COPYFILE_DISABLE=1 tar -C "$stage" -czf "$archive" "$@"
  fi
}

echo "==> Fetching signed Agent release assets"
OBOARD_AGENT_RELEASE_TARGET="$OUT_DIR/agent-release" VERSION="$VERSION_VALUE" "$CONTROLLER_DIR/scripts/fetch-agent-release.sh"

read -r AGENT_VERSION AGENT_BUILD AGENT_COMMIT AGENT_DATE < <(python3 - "$OUT_DIR/agent-release/release-metadata.json" <<'PY'
import json, sys
m = json.load(open(sys.argv[1]))
print(m["version"], m["build"], m["commit"], m["date"])
PY
)
RELEASE_PUBLIC_KEY=${OBOARD_RELEASE_PUBLIC_KEY:?OBOARD_RELEASE_PUBLIC_KEY must contain the Agent release Ed25519 public key}
CONTROLLER_LDFLAGS="-s -w -X github.com/OboardProject/oboard/internal/version.Version=$VERSION_VALUE -X github.com/OboardProject/oboard/internal/version.Build=$BUILD_VALUE -X github.com/OboardProject/oboard/internal/version.Commit=$COMMIT_VALUE -X github.com/OboardProject/oboard/internal/version.Date=$DATE_VALUE -X github.com/OboardProject/oboard/internal/version.ReleasePublicKey=$RELEASE_PUBLIC_KEY -X github.com/OboardProject/oboard/internal/version.AgentVersion=$AGENT_VERSION -X github.com/OboardProject/oboard/internal/version.AgentBuild=$AGENT_BUILD -X github.com/OboardProject/oboard/internal/version.AgentCommit=$AGENT_COMMIT -X github.com/OboardProject/oboard/internal/version.AgentDate=$AGENT_DATE -X github.com/OboardProject/oboard/internal/version.KernelVersion=$AGENT_VERSION -X github.com/OboardProject/oboard/internal/version.KernelBuild=$AGENT_BUILD"

echo "==> Building web assets"
cd "$CONTROLLER_DIR/web"
if [ ! -d node_modules ]; then
  npm ci
fi
rm -rf "$WEB_OUT_DIR"
npm run build -- --outDir "$WEB_OUT_DIR" --emptyOutDir

package_controller() {
  local os=$1 arch=$2 source=$3
  local stage
  stage=$(mktemp -d)
  mkdir -p "$stage/bin" "$stage/deploy/systemd" "$stage/deploy/openrc" "$stage/docs"
  cp "$source" "$stage/bin/oboard-controller"
  cp "$OUT_DIR/bin/$os-$arch/oboard-controller-updater" "$stage/bin/oboard-controller-updater"
  cp "$CONTROLLER_DIR/README.md" "$stage/README.md"
  cp "$CONTROLLER_DIR/LICENSE" "$stage/LICENSE"
  printf '%s\n' "$VERSION_VALUE" > "$stage/VERSION"
  # Optional maintainer note lives in workspace docs/ (not inside the product repo).
  if [ -f "$CONTROLLER_DIR/../docs/RELEASE_GUIDE.md" ]; then
    cp "$CONTROLLER_DIR/../docs/RELEASE_GUIDE.md" "$stage/docs/RELEASE_GUIDE.md"
  fi

  mkdir -p "$stage/web"
  cp -R "$WEB_OUT_DIR" "$stage/web/dist"
  mkdir -p "$stage/downloads"
  for f in "release-manifest.json" "release-manifest.json.sig"; do
    if [ -f "$OUT_DIR/agent-release/$f" ]; then cp "$OUT_DIR/agent-release/$f" "$stage/downloads/$f"; fi
  done
  for f in "$OUT_DIR"/agent-release/oboard-agent-* "$OUT_DIR"/agent-release/oboard-sb-*; do
    if [ -f "$f" ]; then cp "$f" "$stage/downloads/"; fi
  done
  cp "$CONTROLLER_DIR/deploy/systemd/oboard-controller.service" "$stage/deploy/systemd/"
  cp "$CONTROLLER_DIR/deploy/systemd/oboard-controller-updater.service" "$stage/deploy/systemd/"
  cp "$CONTROLLER_DIR/deploy/openrc/oboard-controller" "$stage/deploy/openrc/"
  cp "$CONTROLLER_DIR/deploy/openrc/oboard-controller-updater" "$stage/deploy/openrc/"
  cp "$CONTROLLER_DIR/deploy/controller.env.example" "$stage/deploy/"
  mkdir -p "$stage/deploy/docker"
  cp "$CONTROLLER_DIR/deploy/docker-compose.yml" "$stage/deploy/"
  cp "$CONTROLLER_DIR/deploy/docker/Dockerfile.controller" "$stage/deploy/docker/"
  cp "$CONTROLLER_DIR/deploy/docker/entrypoint.sh" "$stage/deploy/docker/"
  mkdir -p "$stage/scripts"
  cp "$CONTROLLER_DIR/scripts/install-docker.sh" "$stage/scripts/"
  cp "$CONTROLLER_DIR/scripts/update-docker.sh" "$stage/scripts/"

  local archive="$OUT_DIR/oboard_controller_${ARTIFACT_VERSION}_${os}_${arch}.tar.gz"
  local install_archive="$OUT_DIR/oboard_controller_${ARTIFACT_VERSION}_${os}_${arch}_install.tar.gz"
  # Keep the self-update payload compatible with the updater's extraction
  # allowlist. Installation-only service files and scripts live in a separate
  # archive and are never exposed to the privileged self-update extractor.
  create_tar_archive "$stage" "$archive" bin web downloads
  create_tar_archive "$stage" "$install_archive" .
  rm -rf "$stage"
  echo "$archive"
}

for platform in $PLATFORMS; do
  os=${platform%/*}
  arch=${platform#*/}

  echo "==> Building controller $os/$arch"
  mkdir -p "$OUT_DIR/bin/$os-$arch"
  CGO_ENABLED=0 GOOS="$os" GOARCH="$arch" go -C "$CONTROLLER_DIR" build -trimpath -ldflags "$CONTROLLER_LDFLAGS" -o "$OUT_DIR/bin/$os-$arch/oboard-controller" ./cmd/controller
  CGO_ENABLED=0 GOOS="$os" GOARCH="$arch" go -C "$CONTROLLER_DIR" build -trimpath -ldflags "$CONTROLLER_LDFLAGS" -o "$OUT_DIR/bin/$os-$arch/oboard-controller-updater" ./cmd/controller-updater
  package_controller "$os" "$arch" "$OUT_DIR/bin/$os-$arch/oboard-controller" >/dev/null
done

(
  cd "$OUT_DIR"
  find . -maxdepth 1 -name '*.tar.gz' -print0 | sort -z | xargs -0 shasum -a 256 | sed 's#  \./#  #'
) > "$OUT_DIR/sha256sums.txt"
python3 "$CONTROLLER_DIR/scripts/generate-controller-manifest.py" "$OUT_DIR" "$RELEASE_CHANNEL" "$VERSION_VALUE" "$BUILD_VALUE" "$COMMIT_VALUE" "$DATE_VALUE"
echo "==> Release artifacts written to $OUT_DIR"

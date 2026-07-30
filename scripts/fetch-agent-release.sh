#!/usr/bin/env bash
set -euo pipefail

ROOT=$(CDPATH= cd -- "$(dirname -- "$0")/.." && pwd)
REPO=${OBOARD_AGENT_REPO:-OboardProject/oboard-agent}
CHANNEL=${OBOARD_AGENT_CHANNEL:-}
VERSION_VALUE=${VERSION:-$(tr -d '[:space:]' < "$ROOT/VERSION")}
SOURCE=${OBOARD_AGENT_RELEASE_DIR:-}
TARGET=${OBOARD_AGENT_RELEASE_TARGET:-$ROOT/../dist/controller/release/agent-release}
PUBLIC_KEY=${OBOARD_RELEASE_PUBLIC_KEY:-}
EXPECTED_COMMIT=${OBOARD_AGENT_EXPECTED_COMMIT:-}
RELEASE_FILES=(
  oboard-agent-linux-amd64
  oboard-agent-linux-arm64
  oboard-sb-linux-amd64
  oboard-sb-linux-arm64
  release-manifest.json
  release-manifest.json.sig
)

if [ -z "$CHANNEL" ]; then
  case "$VERSION_VALUE" in *dev*) CHANNEL=dev ;; *) CHANNEL=release ;; esac
fi
case "$CHANNEL" in dev|release) ;; *) echo "OBOARD_AGENT_CHANNEL must be dev or release" >&2; exit 2 ;; esac
if [ -z "$PUBLIC_KEY" ]; then
  echo "OBOARD_RELEASE_PUBLIC_KEY must contain the Agent release Ed25519 public key" >&2
  exit 2
fi
if [ -n "$EXPECTED_COMMIT" ] && [[ ! "$EXPECTED_COMMIT" =~ ^[0-9a-f]{40}$ ]]; then
  echo "OBOARD_AGENT_EXPECTED_COMMIT must be a full lowercase 40-character SHA" >&2
  exit 2
fi
if [ "$CHANNEL" = dev ] && [ -z "$SOURCE" ] && [ -z "$EXPECTED_COMMIT" ]; then
  echo "OBOARD_AGENT_EXPECTED_COMMIT is required for remote Agent dev downloads" >&2
  exit 2
fi

expected_version=""
verify_channel=dev
if [ "$CHANNEL" = release ]; then
  expected_version=$VERSION_VALUE
  verify_channel=release
fi

tmp=$(mktemp -d)
trap 'rm -rf "$tmp"' EXIT

copy_release_files() {
  local source=$1 destination=$2 file
  mkdir -p "$destination"
  for file in "${RELEASE_FILES[@]}"; do
    if [ ! -f "$source/$file" ]; then
      echo "Agent release source is missing $file" >&2
      return 1
    fi
    cp "$source/$file" "$destination/$file"
  done
}

verify_release() {
  local directory=$1
  go run "$ROOT/scripts/verify-agent-release.go" \
    --dir "$directory" --public-key "$PUBLIC_KEY" --repo "$REPO" \
    --channel "$verify_channel" --expected-version "$expected_version" \
    --expected-commit "$EXPECTED_COMMIT" \
    > "$directory/release-metadata.json"
}

promote_release() {
  local source=$1 file
  rm -rf "$TARGET"
  mkdir -p "$TARGET"
  for file in "${RELEASE_FILES[@]}"; do
    cp "$source/$file" "$TARGET/$file"
  done
  cp "$source/release-metadata.json" "$TARGET/release-metadata.json"
}

if [ -n "$SOURCE" ]; then
  if [ ! -d "$SOURCE" ]; then
    echo "OBOARD_AGENT_RELEASE_DIR does not exist: $SOURCE" >&2
    exit 2
  fi
  staged="$tmp/source"
  copy_release_files "$SOURCE" "$staged"
  verify_release "$staged"
  promote_release "$staged"
else
  if ! command -v gh >/dev/null 2>&1; then
    echo "gh is required to download Agent release assets; alternatively set OBOARD_AGENT_RELEASE_DIR" >&2
    exit 2
  fi
  if [ -z "${GH_TOKEN:-${OBOARD_GITHUB_TOKEN:-}}" ]; then
    echo "GH_TOKEN or OBOARD_GITHUB_TOKEN is required to download Agent release assets" >&2
    exit 2
  fi
  export GH_TOKEN=${GH_TOKEN:-$OBOARD_GITHUB_TOKEN}
  tag=dev
  if [ "$CHANNEL" = release ]; then
    tag="v$VERSION_VALUE"
  fi
  attempts=1
  delay=0
  if [ "$CHANNEL" = dev ]; then
    attempts=${OBOARD_AGENT_RELEASE_WAIT_ATTEMPTS:-10}
    delay=${OBOARD_AGENT_RELEASE_WAIT_SECONDS:-15}
  fi
  case "$attempts" in *[!0-9]*|0) echo "OBOARD_AGENT_RELEASE_WAIT_ATTEMPTS must be a positive integer" >&2; exit 2 ;; esac
  case "$delay" in *[!0-9]*|"") echo "OBOARD_AGENT_RELEASE_WAIT_SECONDS must be a non-negative integer" >&2; exit 2 ;; esac
  verified=false
  for attempt in $(seq 1 "$attempts"); do
    staged="$tmp/attempt-$attempt"
    mkdir -p "$staged"
    if gh release download "$tag" --repo "$REPO" --dir "$staged" --clobber \
      --pattern 'oboard-agent-linux-*' --pattern 'oboard-sb-linux-*' \
      --pattern release-manifest.json --pattern release-manifest.json.sig \
      && verify_release "$staged"; then
      promote_release "$staged"
      verified=true
      break
    fi
    if [ "$attempt" -lt "$attempts" ]; then
      echo "Agent $tag release is unavailable, incomplete, or does not match the expected commit; retrying in ${delay}s ($attempt/$attempts)" >&2
      sleep "$delay"
    fi
  done
  if [ "$verified" != true ]; then
    echo "Unable to verify Agent release $tag from $REPO at commit ${EXPECTED_COMMIT:-unspecified}" >&2
    exit 1
  fi
fi
echo "==> Verified Agent release assets in $TARGET"

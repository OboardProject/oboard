#!/usr/bin/env bash
set -euo pipefail

ROOT=$(CDPATH= cd -- "$(dirname -- "$0")/.." && pwd)
REPO=${OBOARD_AGENT_REPO:-OboardProject/oboard-agent}
CHANNEL=${OBOARD_AGENT_CHANNEL:-}
VERSION_VALUE=${VERSION:-$(tr -d '[:space:]' < "$ROOT/VERSION")}
SOURCE=${OBOARD_AGENT_RELEASE_DIR:-}
TARGET=${OBOARD_AGENT_RELEASE_TARGET:-$ROOT/../dist/controller/release/agent-release}
PUBLIC_KEY=${OBOARD_RELEASE_PUBLIC_KEY:-}

if [ -z "$CHANNEL" ]; then
  case "$VERSION_VALUE" in *dev*) CHANNEL=dev ;; *) CHANNEL=release ;; esac
fi
case "$CHANNEL" in dev|release) ;; *) echo "OBOARD_AGENT_CHANNEL must be dev or release" >&2; exit 2 ;; esac
if [ -z "$PUBLIC_KEY" ]; then
  echo "OBOARD_RELEASE_PUBLIC_KEY must contain the Agent release Ed25519 public key" >&2
  exit 2
fi

if [ -n "$SOURCE" ]; then
  if [ ! -d "$SOURCE" ]; then
    echo "OBOARD_AGENT_RELEASE_DIR does not exist: $SOURCE" >&2
    exit 2
  fi
  rm -rf "$TARGET"
  mkdir -p "$TARGET"
  for file in oboard-agent-linux-amd64 oboard-agent-linux-arm64 oboard-sb-linux-amd64 oboard-sb-linux-arm64 release-manifest.json release-manifest.json.sig; do
    if [ ! -f "$SOURCE/$file" ]; then
      echo "Agent release source is missing $file" >&2
      exit 2
    fi
    cp "$SOURCE/$file" "$TARGET/$file"
  done
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
  expected_version=""
  if [ "$CHANNEL" = release ]; then
    tag="v$VERSION_VALUE"
    expected_version=$VERSION_VALUE
  fi
  tmp=$(mktemp -d)
  trap 'rm -rf "$tmp"' EXIT
  attempts=1
  delay=0
  if [ "$CHANNEL" = dev ]; then
    attempts=${OBOARD_AGENT_RELEASE_WAIT_ATTEMPTS:-10}
    delay=${OBOARD_AGENT_RELEASE_WAIT_SECONDS:-15}
  fi
  case "$attempts" in *[!0-9]*|0) echo "OBOARD_AGENT_RELEASE_WAIT_ATTEMPTS must be a positive integer" >&2; exit 2 ;; esac
  case "$delay" in *[!0-9]*|"") echo "OBOARD_AGENT_RELEASE_WAIT_SECONDS must be a non-negative integer" >&2; exit 2 ;; esac
  downloaded=false
  for attempt in $(seq 1 "$attempts"); do
    if gh release download "$tag" --repo "$REPO" --dir "$tmp" --clobber \
      --pattern 'oboard-agent-linux-*' --pattern 'oboard-sb-linux-*' \
      --pattern release-manifest.json --pattern release-manifest.json.sig; then
      downloaded=true
      break
    fi
    if [ "$attempt" -lt "$attempts" ]; then
      echo "Agent $tag release is not available yet; retrying in ${delay}s ($attempt/$attempts)" >&2
      sleep "$delay"
    fi
  done
  if [ "$downloaded" != true ]; then
    echo "Unable to download Agent release $tag from $REPO" >&2
    exit 1
  fi
  rm -rf "$TARGET"
  mkdir -p "$TARGET"
  cp "$tmp"/* "$TARGET"/
fi

expected_version=""
verify_channel=dev
if [ "$CHANNEL" = release ]; then
  expected_version=$VERSION_VALUE
  verify_channel=release
fi
go run "$ROOT/scripts/verify-agent-release.go" \
  --dir "$TARGET" --public-key "$PUBLIC_KEY" --repo "$REPO" \
  --channel "$verify_channel" --expected-version "$expected_version" \
  > "$TARGET/release-metadata.json"
echo "==> Verified Agent release assets in $TARGET"

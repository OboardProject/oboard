#!/usr/bin/env bash
set -euo pipefail

ROOT=$(CDPATH= cd -- "$(dirname -- "$0")/.." && pwd)
OUT_DIR=${1:?output directory is required}
MANIFEST="$ROOT/scripts/ip2region-manifest.json"
VERSION=v3.17.0
BASE_URL="https://raw.githubusercontent.com/lionsoul2014/ip2region/$VERSION"

mkdir -p "$OUT_DIR"
cp "$MANIFEST" "$OUT_DIR/manifest.json"

fetch_verified() {
  local path=$1 expected=$2 target=$3 actual
  curl -fsSL --retry 3 --connect-timeout 15 "$BASE_URL/$path" -o "$target.tmp"
  actual=$(shasum -a 256 "$target.tmp" | awk '{print $1}')
  if [ "$actual" != "$expected" ]; then
    rm -f "$target.tmp"
    echo "ip2region checksum mismatch for $path" >&2
    exit 1
  fi
  mv "$target.tmp" "$target"
  chmod 0644 "$target"
}

fetch_verified data/ip2region_v4.xdb 6307a9696f5711f84bcb8b25f07894de68a64a0ed4a1cc7e990562dd3084f210 "$OUT_DIR/ip2region_v4.xdb"
fetch_verified data/ip2region_v6.xdb 5b93da35ac28bc316dccc54a758381f7a874ae0461dd51ff5df5e34815586f11 "$OUT_DIR/ip2region_v6.xdb"
fetch_verified LICENSE.md fe01f2f8fcaafac539154e6aa80b0b7f8af54e01dc4d52322f72971991c6280e "$OUT_DIR/LICENSE.md"

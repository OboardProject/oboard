#!/usr/bin/env sh
set -eu

REPO=${OBOARD_REPO:-OboardProject/oboard}
url="https://raw.githubusercontent.com/$REPO/main/scripts/install-docker.sh"
root=${OBOARD_DOCKER_DIR:-/opt/oboard-docker}
version=${VERSION:-$(sed -n 's/^OBOARD_TAG=//p' "$root/.env" 2>/dev/null | tail -n1)}
if [ -f "$(dirname "$0")/install-docker.sh" ]; then
  exec env OBOARD_ACTION=update OBOARD_DOCKER_DIR="$root" VERSION="${version:-latest}" sh "$(dirname "$0")/install-docker.sh"
fi
curl -fsSL "$url" | env OBOARD_ACTION=update OBOARD_DOCKER_DIR="$root" VERSION="${version:-latest}" sh

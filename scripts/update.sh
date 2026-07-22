#!/usr/bin/env bash
set -euo pipefail

COMPONENT=${COMPONENT:-${1:-controller}}
VERSION_INPUT=${VERSION:-}
REPO=${OBOARD_REPO:-OboardProject/oboard}
export COMPONENT OBOARD_REPO="$REPO"
if [ "${OBOARD_INSTALL_METHOD:-binary}" = docker ]; then
  SCRIPT_DIR=$(CDPATH= cd -- "$(dirname -- "$0")" && pwd)
  if [ -f "$SCRIPT_DIR/install-docker.sh" ]; then
    if [ -z "$VERSION_INPUT" ]; then
      exec env -u VERSION OBOARD_ACTION=update sh "$SCRIPT_DIR/install-docker.sh"
    fi
    exec env OBOARD_ACTION=update VERSION="$VERSION_INPUT" sh "$SCRIPT_DIR/install-docker.sh"
  fi
  if [ -z "$VERSION_INPUT" ]; then
    curl -fsSL "https://raw.githubusercontent.com/$REPO/main/scripts/install-docker.sh" | env -u VERSION OBOARD_ACTION=update sh
    exit $?
  fi
  curl -fsSL "https://raw.githubusercontent.com/$REPO/main/scripts/install-docker.sh" | env OBOARD_ACTION=update VERSION="$VERSION_INPUT" sh
  exit $?
fi
VERSION=${VERSION_INPUT:-latest}
export VERSION
case "$COMPONENT" in
  agent|agent-sb|node|controller-agent|all) export OBOARD_AGENT_ACTION=update ;;
esac

SCRIPT_DIR=$(CDPATH= cd -- "$(dirname -- "$0")" && pwd)
if [ -x "$SCRIPT_DIR/install.sh" ]; then
  "$SCRIPT_DIR/install.sh" "$COMPONENT"
else
  curl --proto '=https' --tlsv1.2 -fsSL "https://raw.githubusercontent.com/$REPO/main/scripts/install.sh" | bash -s -- "$COMPONENT"
fi

restart_systemd() {
  case "$COMPONENT" in
    controller) systemctl restart oboard-controller ;;
    agent|agent-sb|node)
      systemctl restart oboard-sb || true
      if [ "${OBOARD_AGENT_RESTART:-now}" = delayed ]; then
        nohup sh -c 'sleep 8; systemctl restart oboard-agent || true' >/dev/null 2>&1 &
        echo "Agent 将在后台完成重启。"
      else
        systemctl restart oboard-agent || true
      fi
      ;;
    controller-agent|all)
      systemctl restart oboard-controller || true
      systemctl restart oboard-agent || true
      systemctl restart oboard-sb || true
      ;;
  esac
}

restart_openrc() {
  case "$COMPONENT" in
    controller) rc-service oboard-controller restart ;;
    agent|agent-sb|node)
      rc-service oboard-sb restart || true
      if [ "${OBOARD_AGENT_RESTART:-now}" = delayed ]; then
        nohup sh -c 'sleep 8; rc-service oboard-agent restart || true' >/dev/null 2>&1 &
        echo "Agent 将在后台完成重启。"
      else
        rc-service oboard-agent restart || true
      fi
      ;;
    controller-agent|all)
      rc-service oboard-controller restart || true
      rc-service oboard-agent restart || true
      rc-service oboard-sb restart || true
      ;;
  esac
}

if command -v systemctl >/dev/null 2>&1 && [ -d /run/systemd/system ]; then
  restart_systemd
elif command -v rc-service >/dev/null 2>&1; then
  restart_openrc
fi

echo "OBoard $COMPONENT 已更新到 $VERSION。"

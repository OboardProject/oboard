#!/bin/sh
set -eu
(set -o pipefail) 2>/dev/null && set -o pipefail

COMPONENT=${COMPONENT:-${1:-controller}}
VERSION_INPUT=${VERSION:-}
REPO=${OBOARD_REPO:-OboardProject/oboard}
export COMPONENT OBOARD_REPO="$REPO" OBOARD_ACTION=update
if [ -n "$VERSION_INPUT" ]; then
  VERSION=$VERSION_INPUT
  export VERSION
else
  unset VERSION
fi
case "$COMPONENT" in
  agent|agent-sb|node|controller-agent|all) export OBOARD_AGENT_ACTION=update ;;
esac

SCRIPT_DIR=
SCRIPT_FILE=${0:-}
if [ -n "$SCRIPT_FILE" ] && [ -f "$SCRIPT_FILE" ]; then
  SCRIPT_DIR=$(CDPATH= cd -- "$(dirname -- "$SCRIPT_FILE")" && pwd)
fi
case "$COMPONENT" in
  controller)
    if [ -n "$SCRIPT_DIR" ] && [ -x "$SCRIPT_DIR/install.sh" ]; then
      exec "$SCRIPT_DIR/install.sh" "$COMPONENT"
    fi
    curl --proto '=https' --tlsv1.2 -fsSL "https://raw.githubusercontent.com/$REPO/main/scripts/install.sh" | sh -s -- "$COMPONENT"
    exit $?
    ;;
  *)
    if [ -n "$SCRIPT_DIR" ] && [ -x "$SCRIPT_DIR/install.sh" ]; then
      "$SCRIPT_DIR/install.sh" "$COMPONENT"
    else
      curl --proto '=https' --tlsv1.2 -fsSL "https://raw.githubusercontent.com/$REPO/main/scripts/install.sh" | sh -s -- "$COMPONENT"
    fi
    ;;
esac

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

echo "OBoard $COMPONENT 更新完成。"

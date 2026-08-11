#!/bin/sh
set -eu
(set -o pipefail) 2>/dev/null && set -o pipefail

REPO=${OBOARD_REPO:-OboardProject/oboard}
VERSION_VALUE=${VERSION:-latest}
ACTION=${OBOARD_ACTION:-install}
MANAGED_UPDATE=${OBOARD_MANAGED_UPDATE:-0}
INSTALL_DIR=/opt/oboard-subscription-relay
ENV_FILE=$INSTALL_DIR/relay.env
TMP_DIR=

cleanup() {
	status=$?
	[ -z "$TMP_DIR" ] || rm -rf "$TMP_DIR"
	exit "$status"
}
trap cleanup EXIT

fail() { echo "$*" >&2; exit 1; }
need_command() { command -v "$1" >/dev/null 2>&1 || fail "缺少命令：$1"; }

resolve_release_target() {
	VERSION_VALUE=${VERSION_VALUE#v}
	case "$VERSION_VALUE" in
		*dev*) TAG=dev; ARTIFACT_VERSION=dev ;;
		*) TAG=v$VERSION_VALUE; ARTIFACT_VERSION=$VERSION_VALUE ;;
	esac
}

[ "$(id -u)" -eq 0 ] || fail "安装订阅中继需要 root 权限。"
case "$REPO" in [A-Za-z0-9_.-]*/[A-Za-z0-9_.-]*) ;; *) fail "OBOARD_REPO 格式无效。" ;; esac
case "$ACTION" in install|update|uninstall) ;; *) fail "OBOARD_ACTION 必须是 install、update 或 uninstall。" ;; esac
case "$MANAGED_UPDATE" in 0|1) ;; *) fail "OBOARD_MANAGED_UPDATE 必须是 0 或 1。" ;; esac

service_manager() {
	if command -v systemctl >/dev/null 2>&1 && [ -d /run/systemd/system ]; then echo systemd
	elif command -v rc-service >/dev/null 2>&1; then echo openrc
	else echo unknown
	fi
}

env_value() {
	key=$1
	[ -r "$ENV_FILE" ] || return 0
	sed -n "s/^${key}=//p" "$ENV_FILE" | tail -n1
}

uninstall_relay() {
	stored_controller_url=$(env_value OBOARD_CONTROLLER_URL)
	stored_relay_id=$(env_value OBOARD_SUBSCRIPTION_RELAY_ID)
	stored_relay_token=$(env_value OBOARD_SUBSCRIPTION_RELAY_TOKEN)
	stored_relay_secret=$(env_value OBOARD_SUBSCRIPTION_RELAY_SECRET)
	if [ -x "$INSTALL_DIR/oboard-subscription-relay" ]; then
		OBOARD_CONTROLLER_URL=$stored_controller_url OBOARD_SUBSCRIPTION_RELAY_ID=$stored_relay_id OBOARD_SUBSCRIPTION_RELAY_TOKEN=$stored_relay_token OBOARD_SUBSCRIPTION_RELAY_SECRET=$stored_relay_secret "$INSTALL_DIR/oboard-subscription-relay" notify-uninstall >/dev/null 2>&1 || true
	fi
	manager=$(service_manager)
	if [ "$manager" = systemd ]; then
		systemctl disable --now oboard-subscription-relay-updater.service oboard-subscription-relay.service >/dev/null 2>&1 || true
		rm -f /etc/systemd/system/oboard-subscription-relay-updater.service /etc/systemd/system/oboard-subscription-relay.service
		systemctl daemon-reload >/dev/null 2>&1 || true
	elif [ "$manager" = openrc ]; then
		rc-service oboard-subscription-relay-updater stop >/dev/null 2>&1 || true
		rc-service oboard-subscription-relay stop >/dev/null 2>&1 || true
		rc-update del oboard-subscription-relay-updater default >/dev/null 2>&1 || true
		rc-update del oboard-subscription-relay default >/dev/null 2>&1 || true
		rm -f /etc/init.d/oboard-subscription-relay-updater /etc/init.d/oboard-subscription-relay
	fi
	rm -rf "$INSTALL_DIR"
	echo "订阅中继已卸载。可以在主控中删除对应记录。"
}

if [ "$ACTION" = uninstall ]; then uninstall_relay; exit 0; fi

stored_controller_url=$(env_value OBOARD_CONTROLLER_URL)
stored_relay_addr=$(env_value OBOARD_SUBSCRIPTION_RELAY_ADDR)
stored_trusted_proxies=$(env_value OBOARD_SUBSCRIPTION_RELAY_TRUSTED_PROXY_CIDRS)
CONTROLLER_URL=${OBOARD_CONTROLLER_URL:-$stored_controller_url}
ENROLLMENT_TOKEN=${OBOARD_SUBSCRIPTION_RELAY_ENROLLMENT_TOKEN:-}
RELAY_ADDR=${OBOARD_SUBSCRIPTION_RELAY_ADDR:-${stored_relay_addr:-:2777}}
TRUSTED_PROXIES=${OBOARD_SUBSCRIPTION_RELAY_TRUSTED_PROXY_CIDRS:-${stored_trusted_proxies:-127.0.0.0/8,::1/128}}
[ -n "$CONTROLLER_URL" ] || fail "缺少 OBOARD_CONTROLLER_URL。"
case "$CONTROLLER_URL" in https://*) ;; *) fail "OBOARD_CONTROLLER_URL 必须使用 HTTPS。" ;; esac
case "$CONTROLLER_URL" in *[!A-Za-z0-9:/._~-]*) fail "OBOARD_CONTROLLER_URL 包含不安全字符。" ;; esac
case "$RELAY_ADDR" in *[!A-Za-z0-9:.[\]-]*) fail "中继监听地址包含不安全字符。" ;; esac
case "$TRUSTED_PROXIES" in *[!A-Fa-f0-9:.,/-]*) fail "可信代理 CIDR 包含不安全字符。" ;; esac
if [ "$ACTION" = install ]; then
	[ -n "$ENROLLMENT_TOKEN" ] || fail "缺少一次性中继接入令牌。"
	case "$ENROLLMENT_TOKEN" in *[!A-Za-z0-9_-]*) fail "中继接入令牌格式无效。" ;; esac
elif [ ! -r "$ENV_FILE" ]; then
	fail "未找到现有中继配置，不能执行更新。"
fi

case "$(uname -m)" in x86_64|amd64) ARCH=amd64 ;; aarch64|arm64) ARCH=arm64 ;; *) fail "不支持当前架构：$(uname -m)" ;; esac
need_command tar
if command -v curl >/dev/null 2>&1; then
	download() { curl -fsSL --retry 3 --connect-timeout 15 "$1" -o "$2"; }
	if [ "$VERSION_VALUE" = latest ]; then
		resolved=$(curl -fsSLI -o /dev/null -w '%{url_effective}' "https://github.com/$REPO/releases/latest")
		VERSION_VALUE=${resolved##*/tag/}
	fi
elif command -v wget >/dev/null 2>&1; then
	download() { wget -q -O "$2" "$1"; }
	[ "$VERSION_VALUE" != latest ] || fail "使用 wget 时请通过 VERSION 指定版本。"
else
	fail "需要 curl 或 wget。"
fi
resolve_release_target
ARCHIVE=oboard_controller_${ARTIFACT_VERSION}_linux_${ARCH}_install.tar.gz
BASE_URL=https://github.com/$REPO/releases/download/$TAG
TMP_DIR=$(mktemp -d /tmp/oboard-subscription-relay.XXXXXX)
download "$BASE_URL/$ARCHIVE" "$TMP_DIR/$ARCHIVE"
download "$BASE_URL/sha256sums.txt" "$TMP_DIR/sha256sums.txt"

expected=$(awk -v name="$ARCHIVE" '$2 == name {print $1}' "$TMP_DIR/sha256sums.txt")
[ -n "$expected" ] || fail "发布校验和中缺少 $ARCHIVE。"
if command -v sha256sum >/dev/null 2>&1; then actual=$(sha256sum "$TMP_DIR/$ARCHIVE" | awk '{print $1}')
else need_command shasum; actual=$(shasum -a 256 "$TMP_DIR/$ARCHIVE" | awk '{print $1}'); fi
[ "$actual" = "$expected" ] || fail "发布包校验失败。"

tar -tzf "$TMP_DIR/$ARCHIVE" | awk 'BEGIN {bad=0} /^\// || /(^|\/)\.\.($|\/)/ {bad=1} END {exit bad}' || fail "发布包包含不安全路径。"
tar -xzf "$TMP_DIR/$ARCHIVE" -C "$TMP_DIR"
[ -x "$TMP_DIR/bin/oboard-subscription-relay" ] || fail "发布包缺少订阅中继程序。"
install -d -m 0755 "$INSTALL_DIR"
install -m 0755 "$TMP_DIR/bin/oboard-subscription-relay" "$INSTALL_DIR/oboard-subscription-relay.new"
mv -f "$INSTALL_DIR/oboard-subscription-relay.new" "$INSTALL_DIR/oboard-subscription-relay"

if [ "$ACTION" = install ]; then
	"$INSTALL_DIR/oboard-subscription-relay" enroll --controller "$CONTROLLER_URL" --token "$ENROLLMENT_TOKEN" --env-output "$ENV_FILE"
	{
		printf 'OBOARD_SUBSCRIPTION_RELAY_ADDR=%s\n' "$RELAY_ADDR"
		printf 'OBOARD_SUBSCRIPTION_RELAY_TRUSTED_PROXY_CIDRS=%s\n' "$TRUSTED_PROXIES"
	} >> "$ENV_FILE"
	chmod 0600 "$ENV_FILE"
fi

manager=$(service_manager)
if [ "$manager" = systemd ]; then
	if [ "$MANAGED_UPDATE" = 0 ]; then
		install -m 0644 "$TMP_DIR/deploy/systemd/oboard-subscription-relay.service" /etc/systemd/system/oboard-subscription-relay.service
		install -m 0644 "$TMP_DIR/deploy/systemd/oboard-subscription-relay-updater.service" /etc/systemd/system/oboard-subscription-relay-updater.service
		systemctl daemon-reload
		systemctl enable oboard-subscription-relay.service oboard-subscription-relay-updater.service
	fi
	systemctl restart oboard-subscription-relay.service
	[ "$MANAGED_UPDATE" = 1 ] || systemctl restart oboard-subscription-relay-updater.service
elif [ "$manager" = openrc ]; then
	if [ "$MANAGED_UPDATE" = 0 ]; then
		install -m 0755 "$TMP_DIR/deploy/openrc/oboard-subscription-relay" /etc/init.d/oboard-subscription-relay
		install -m 0755 "$TMP_DIR/deploy/openrc/oboard-subscription-relay-updater" /etc/init.d/oboard-subscription-relay-updater
		rc-update add oboard-subscription-relay default
		rc-update add oboard-subscription-relay-updater default
	fi
	rc-service oboard-subscription-relay restart
	[ "$MANAGED_UPDATE" = 1 ] || rc-service oboard-subscription-relay-updater restart
else
	fail "未识别 systemd 或 OpenRC；程序已安装但尚未启动。"
fi

if [ "$ACTION" = install ]; then
	echo "订阅中继已接入并启动，请确认 HTTPS 反向代理指向本机监听端口。"
else
	echo "订阅中继已更新到 $VERSION_VALUE。"
fi

#!/bin/sh
set -eu
(set -o pipefail) 2>/dev/null && set -o pipefail

REPO=${OBOARD_REPO:-OboardProject/oboard}
VERSION_VALUE=${VERSION:-latest}
ACTION=${OBOARD_ACTION:-install}
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

[ "$(id -u)" -eq 0 ] || fail "安装订阅中继需要 root 权限。"
case "$REPO" in [A-Za-z0-9_.-]*/[A-Za-z0-9_.-]*) ;; *) fail "OBOARD_REPO 格式无效。" ;; esac
case "$ACTION" in install|update|uninstall) ;; *) fail "OBOARD_ACTION 必须是 install、update 或 uninstall。" ;; esac

service_manager() {
	if command -v systemctl >/dev/null 2>&1 && [ -d /run/systemd/system ]; then echo systemd
	elif command -v rc-service >/dev/null 2>&1; then echo openrc
	else echo unknown
	fi
}

uninstall_relay() {
	manager=$(service_manager)
	if [ "$manager" = systemd ]; then
		systemctl disable --now oboard-subscription-relay.service >/dev/null 2>&1 || true
		rm -f /etc/systemd/system/oboard-subscription-relay.service
		systemctl daemon-reload >/dev/null 2>&1 || true
	elif [ "$manager" = openrc ]; then
		rc-service oboard-subscription-relay stop >/dev/null 2>&1 || true
		rc-update del oboard-subscription-relay default >/dev/null 2>&1 || true
		rm -f /etc/init.d/oboard-subscription-relay
	fi
	rm -rf "$INSTALL_DIR"
	echo "订阅中继已卸载。主控中的中继公开地址需要另行清空。"
}

if [ "$ACTION" = uninstall ]; then uninstall_relay; exit 0; fi

stored_controller_url=
stored_relay_secret=
stored_relay_addr=
stored_trusted_proxies=
if [ "$ACTION" = update ] && [ -r "$ENV_FILE" ]; then
	stored_controller_url=$(sed -n 's/^OBOARD_CONTROLLER_URL=//p' "$ENV_FILE" | tail -n1)
	stored_relay_secret=$(sed -n 's/^OBOARD_SUBSCRIPTION_RELAY_SECRET=//p' "$ENV_FILE" | tail -n1)
	stored_relay_addr=$(sed -n 's/^OBOARD_SUBSCRIPTION_RELAY_ADDR=//p' "$ENV_FILE" | tail -n1)
	stored_trusted_proxies=$(sed -n 's/^OBOARD_SUBSCRIPTION_RELAY_TRUSTED_PROXY_CIDRS=//p' "$ENV_FILE" | tail -n1)
fi
CONTROLLER_URL=${OBOARD_CONTROLLER_URL:-$stored_controller_url}
RELAY_SECRET=${OBOARD_SUBSCRIPTION_RELAY_SECRET:-$stored_relay_secret}
RELAY_ADDR=${OBOARD_SUBSCRIPTION_RELAY_ADDR:-${stored_relay_addr:-:8080}}
TRUSTED_PROXIES=${OBOARD_SUBSCRIPTION_RELAY_TRUSTED_PROXY_CIDRS:-${stored_trusted_proxies:-127.0.0.0/8,::1/128}}
[ -n "$CONTROLLER_URL" ] || fail "缺少 OBOARD_CONTROLLER_URL。"
case "$CONTROLLER_URL" in https://*) ;; *) fail "OBOARD_CONTROLLER_URL 必须使用 HTTPS。" ;; esac
[ "${#RELAY_SECRET}" -ge 32 ] || fail "OBOARD_SUBSCRIPTION_RELAY_SECRET 至少需要 32 个字符。"
case "$CONTROLLER_URL" in *[!A-Za-z0-9:/._~-]*) fail "OBOARD_CONTROLLER_URL 包含不安全字符。" ;; esac
case "$RELAY_SECRET" in *[!A-Za-z0-9._~+=/-]*) fail "中继密钥只能使用 URL 安全字符。" ;; esac
case "$RELAY_ADDR" in *[!A-Za-z0-9:.[\]-]*) fail "中继监听地址包含不安全字符。" ;; esac
case "$TRUSTED_PROXIES" in *[!A-Fa-f0-9:.,/-]*) fail "可信代理 CIDR 包含不安全字符。" ;; esac

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
	[ "$VERSION_VALUE" != latest ] || fail "使用 wget 安装时请通过 VERSION 指定版本。"
else
	fail "需要 curl 或 wget。"
fi
VERSION_VALUE=${VERSION_VALUE#v}
TAG=v$VERSION_VALUE
ARTIFACT_VERSION=$VERSION_VALUE
if [ "$VERSION_VALUE" = dev ]; then TAG=dev; ARTIFACT_VERSION=dev; fi
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
umask 077
{
	printf 'OBOARD_CONTROLLER_URL=%s\n' "$CONTROLLER_URL"
	printf 'OBOARD_SUBSCRIPTION_RELAY_SECRET=%s\n' "$RELAY_SECRET"
	printf 'OBOARD_SUBSCRIPTION_RELAY_ADDR=%s\n' "$RELAY_ADDR"
	printf 'OBOARD_SUBSCRIPTION_RELAY_TRUSTED_PROXY_CIDRS=%s\n' "$TRUSTED_PROXIES"
} > "$ENV_FILE.new"
mv -f "$ENV_FILE.new" "$ENV_FILE"

manager=$(service_manager)
if [ "$manager" = systemd ]; then
	install -m 0644 "$TMP_DIR/deploy/systemd/oboard-subscription-relay.service" /etc/systemd/system/oboard-subscription-relay.service
	systemctl daemon-reload
	systemctl enable --now oboard-subscription-relay.service
elif [ "$manager" = openrc ]; then
	install -m 0755 "$TMP_DIR/deploy/openrc/oboard-subscription-relay" /etc/init.d/oboard-subscription-relay
	rc-update add oboard-subscription-relay default
	rc-service oboard-subscription-relay restart
else
	fail "未识别 systemd 或 OpenRC；程序已安装但尚未启动。"
fi
echo "订阅中继已启动，请为其配置 HTTPS 反向代理后在面板中填写中继公开地址。"

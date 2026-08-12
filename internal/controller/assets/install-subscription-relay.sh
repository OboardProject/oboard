#!/bin/sh
set -eu
(set -o pipefail) 2>/dev/null && set -o pipefail

VERSION_VALUE=${VERSION:-latest}
ACTION=${OBOARD_ACTION:-install}
MANAGED_UPDATE=${OBOARD_MANAGED_UPDATE:-0}
INSTALL_DIR=/opt/oboard-subscription-relay
ENV_FILE=$INSTALL_DIR/relay.env
TMP_DIR=

cleanup() {
	status=$?
	[ -z "$TMP_DIR" ] || rm -rf "$TMP_DIR"
	if [ "$status" -ne 0 ]; then
		echo "" >&2
		echo "OBoard 订阅中继操作未完成。" >&2
		echo "请根据上方提示处理后重试。" >&2
	fi
	trap - EXIT
	exit "$status"
}
trap cleanup EXIT

fail() { echo "$*" >&2; exit 1; }
need_command() { command -v "$1" >/dev/null 2>&1 || fail "缺少命令：$1"; }

[ "$(id -u)" -eq 0 ] || fail "安装订阅中继需要 root 权限。"
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
	manager=$(service_manager)
	echo "OBoard 订阅中继"
	echo "----------------"
	echo "正在卸载订阅中继。"
	echo "安装目录：$INSTALL_DIR"
	echo "服务管理器：$manager"
	echo ""
	echo "[1/2] 通知主控并停止中继服务"
	if [ -x "$INSTALL_DIR/oboard-subscription-relay" ]; then
		OBOARD_CONTROLLER_URL=$stored_controller_url OBOARD_SUBSCRIPTION_RELAY_ID=$stored_relay_id OBOARD_SUBSCRIPTION_RELAY_TOKEN=$stored_relay_token OBOARD_SUBSCRIPTION_RELAY_SECRET=$stored_relay_secret "$INSTALL_DIR/oboard-subscription-relay" notify-uninstall >/dev/null 2>&1 || true
	fi
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
	echo "[2/2] 删除中继程序和本机配置"
	rm -rf "$INSTALL_DIR"
	echo ""
	echo "OBoard 订阅中继卸载完成"
	echo "------------------------"
	echo "本机程序和接入凭据已删除，可以在主控中删除对应记录。"
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

echo "OBoard 订阅中继"
echo "----------------"
if [ "$ACTION" = update ]; then
	echo "正在更新，现有接入配置将保留。"
else
	echo "正在开始安装。"
fi
echo "主控地址：$CONTROLLER_URL"
echo "安装目录：$INSTALL_DIR"
echo "监听地址：$RELAY_ADDR"
echo ""
echo "[1/4] 检查运行环境"
case "$(uname -m)" in x86_64|amd64) ARCH=amd64 ;; aarch64|arm64) ARCH=arm64 ;; *) fail "不支持当前架构：$(uname -m)" ;; esac
need_command tar
need_command install
manager=$(service_manager)
echo "环境：linux/$ARCH 服务管理器=$manager"
if command -v curl >/dev/null 2>&1; then
	download() { curl -fSsL --retry 3 --connect-timeout 15 "$1" -o "$2" || { echo "下载失败：$1" >&2; return 1; }; }
elif command -v wget >/dev/null 2>&1; then
	download() { wget -q -O "$2" "$1" || { echo "下载失败：$1" >&2; return 1; }; }
else
	fail "需要 curl 或 wget。"
fi
echo "[2/4] 从主控下载中继组件"
echo "目标版本：$VERSION_VALUE"
ARCHIVE=oboard-subscription-relay-linux-${ARCH}.tar.gz
BASE_URL=${CONTROLLER_URL%/}/downloads
TMP_DIR=$(mktemp -d /tmp/oboard-subscription-relay.XXXXXX)
download "$BASE_URL/$ARCHIVE" "$TMP_DIR/$ARCHIVE" || fail "无法从主控下载 $ARCHIVE。"
download "$BASE_URL/subscription-relay-sha256s.txt" "$TMP_DIR/sha256sums.txt" || fail "无法从主控下载订阅中继校验文件。"

echo "[3/4] 校验并安装中继组件"
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
	echo "[4/4] 接入主控并启动中继服务"
	"$INSTALL_DIR/oboard-subscription-relay" enroll --controller "$CONTROLLER_URL" --token "$ENROLLMENT_TOKEN" --env-output "$ENV_FILE"
	{
		printf 'OBOARD_SUBSCRIPTION_RELAY_ADDR=%s\n' "$RELAY_ADDR"
		printf 'OBOARD_SUBSCRIPTION_RELAY_TRUSTED_PROXY_CIDRS=%s\n' "$TRUSTED_PROXIES"
	} >> "$ENV_FILE"
	chmod 0600 "$ENV_FILE"
else
	echo "[4/4] 刷新中继服务"
fi

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

echo ""
if [ "$ACTION" = install ]; then
	echo "OBoard 订阅中继安装完成"
else
	echo "OBoard 订阅中继更新完成"
fi
echo "------------------------"
echo "当前版本：$VERSION_VALUE"
echo "监听地址：$RELAY_ADDR"
echo "服务管理器：$manager"
if [ "$manager" = systemd ]; then
	echo "查看状态：systemctl status oboard-subscription-relay --no-pager"
	echo "查看日志：journalctl -u oboard-subscription-relay -f"
else
	echo "查看状态：rc-service oboard-subscription-relay status"
	echo "查看日志：tail -f /var/log/oboard-subscription-relay.log"
fi
if [ "$ACTION" = install ]; then
	echo "请确认 HTTPS 反向代理已指向本机监听地址。"
else
	echo "现有接入配置已保留，中继服务已重新启动。"
fi

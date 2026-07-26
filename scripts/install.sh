#!/usr/bin/env bash
set -euo pipefail

REPO=${OBOARD_REPO:-OboardProject/oboard}
# Refuse obviously malicious repo values used in download URLs.
case "$REPO" in
  [A-Za-z0-9_.-]*/[A-Za-z0-9_.-]*) ;;
  *) echo "OBOARD_REPO must look like owner/name" >&2; exit 1 ;;
esac
VERSION_INPUT=${VERSION:-}
VERSION_VALUE=${VERSION_INPUT:-latest}
INSTALL_CHANNEL=stable
COMPONENT=${COMPONENT:-${1:-controller}}
INSTALL_DIR=${INSTALL_DIR:-/usr/local/bin}
ACTION_INPUT=${OBOARD_ACTION:-}
ACTION=install
INSTALLATION_EXISTS=0
TMP_DIR=
CONTROLLER_DATA_EXISTED=0
ADMIN_USERNAME_INPUT=${OBOARD_ADMIN_USERNAME:-}
ADMIN_PASSWORD_INPUT=${OBOARD_ADMIN_PASSWORD:-}
BOOTSTRAP_ADMIN_USERNAME=
BOOTSTRAP_ADMIN_PASSWORD_CONFIGURED=0
[ -s /var/lib/oboard/oboard.sqlite ] && CONTROLLER_DATA_EXISTED=1

make_install_tmp() {
  local base candidate available_kb
  for base in "${OBOARD_TMPDIR:-}" /var/tmp /tmp /run; do
    [ -n "$base" ] || continue
    mkdir -p "$base" 2>/dev/null || continue
    candidate=$(mktemp -d "$base/oboard-controller-install.XXXXXX" 2>/dev/null || true)
    [ -n "$candidate" ] || continue
    available_kb=$(df -Pk "$candidate" 2>/dev/null | awk 'NR==2 {print $4}')
    if [ -z "$available_kb" ] || [ "$available_kb" -ge 65536 ]; then
      printf '%s\n' "$candidate"
      return 0
    fi
    rm -rf "$candidate"
  done
  echo "没有可用的安装临时目录，需要至少 64 MB 可用空间。" >&2
  echo "可清理 /var/tmp、/tmp、/run，或通过 OBOARD_TMPDIR 指定其他目录。" >&2
  df -h / /var/tmp /tmp /run 2>/dev/null >&2 || true
  df -i / /var/tmp /tmp /run 2>/dev/null >&2 || true
  return 1
}

binary_installation_exists() {
  [ -f /etc/oboard/controller.env ] ||
    [ -x "$INSTALL_DIR/oboard-controller" ] ||
    [ -s /var/lib/oboard/oboard.sqlite ] ||
    [ -f /etc/systemd/system/oboard-controller.service ] ||
    [ -f /etc/init.d/oboard-controller ]
}

select_installation() {
  binary_installation_exists && INSTALLATION_EXISTS=1

  case "$ACTION_INPUT" in
    ""|install|update|uninstall) ;;
    *) echo "操作方式无效，请选择安装、更新或卸载。" >&2; exit 1 ;;
  esac
  if [ "$INSTALLATION_EXISTS" = 1 ] && [ "$ACTION_INPUT" != uninstall ]; then
    ACTION=update
  else
    ACTION=${ACTION_INPUT:-install}
  fi
  if [ "$ACTION" = update ] && [ "$INSTALLATION_EXISTS" = 0 ]; then
    echo "没有找到已安装的主控，请先完成安装。" >&2
    exit 1
  fi
}

cleanup() { [ -z "$TMP_DIR" ] || rm -rf "$TMP_DIR"; }

drain_piped_script() {
  if [ ! -t 0 ]; then
    cat >/dev/null || true
  fi
}

need_root() {
  if [ "$(id -u)" -ne 0 ]; then
    echo "安装需要写入系统目录，请切换到 root 或使用 sudo 重新执行。" >&2
    exit 1
  fi
}

pkg_install() {
  if [ "$#" -eq 0 ]; then
    return 0
  fi
  if command -v apk >/dev/null 2>&1; then
    apk add --no-cache "$@"
  elif command -v apt-get >/dev/null 2>&1; then
    export DEBIAN_FRONTEND=noninteractive
    apt-get update -y
    apt-get install -y --no-install-recommends "$@"
  elif command -v dnf >/dev/null 2>&1; then
    dnf install -y "$@"
  elif command -v yum >/dev/null 2>&1; then
    yum install -y "$@"
  elif command -v microdnf >/dev/null 2>&1; then
    microdnf install -y "$@"
  elif command -v zypper >/dev/null 2>&1; then
    zypper --non-interactive install -y "$@"
  elif command -v pacman >/dev/null 2>&1; then
    pacman -Sy --noconfirm "$@"
  else
    echo "未找到支持的包管理器，无法自动安装：$*" >&2
    return 1
  fi
}

ensure_base_tools() {
  need_curl=0
  need_ca=0
  need_tar=0
  need_sha=0
  command -v curl >/dev/null 2>&1 || need_curl=1
  command -v tar >/dev/null 2>&1 || need_tar=1
  if ! command -v sha256sum >/dev/null 2>&1 && ! command -v shasum >/dev/null 2>&1; then
    need_sha=1
  fi
  if [ ! -f /etc/ssl/certs/ca-certificates.crt ] && [ ! -f /etc/pki/tls/certs/ca-bundle.crt ] && [ ! -f /etc/ssl/cert.pem ]; then
    need_ca=1
  fi
  if [ "$need_curl$need_ca$need_tar$need_sha" = "0000" ]; then
    return 0
  fi
  echo "正在安装所需组件（curl / CA 证书 / tar / 校验工具）..."
  packages=""
  [ "$need_curl" = 1 ] && packages="$packages curl"
  [ "$need_ca" = 1 ] && packages="$packages ca-certificates"
  [ "$need_tar" = 1 ] && packages="$packages tar"
  if [ "$need_sha" = 1 ]; then
    if command -v apk >/dev/null 2>&1; then
      packages="$packages coreutils"
    elif command -v apt-get >/dev/null 2>&1; then
      packages="$packages coreutils"
    else
      packages="$packages coreutils"
    fi
  fi
  # shellcheck disable=SC2086
  pkg_install $packages || {
    echo "依赖安装失败，请手动安装 curl、ca-certificates、tar、sha256sum 后重试。" >&2
    exit 1
  }
  if command -v update-ca-certificates >/dev/null 2>&1; then
    update-ca-certificates >/dev/null 2>&1 || true
  elif command -v update-ca-trust >/dev/null 2>&1; then
    update-ca-trust extract >/dev/null 2>&1 || true
  fi
}

ensure_acme_sh() {
  if command -v acme.sh >/dev/null 2>&1; then
    return 0
  fi
  echo "正在安装证书签发依赖（acme.sh / openssl / socat）..."
  if ! pkg_install acme.sh openssl socat; then
    echo "当前发行版仓库未提供 acme.sh。请先安装 acme.sh、openssl、socat，并确保 acme.sh 位于 PATH 后重试。" >&2
    exit 1
  fi
  if ! command -v acme.sh >/dev/null 2>&1; then
    echo "安装后仍找不到 acme.sh，请检查 PATH。" >&2
    exit 1
  fi
}

detect_virt_hint() {
  if [ -f /run/.containerenv ]; then
    echo container
    return
  fi
  if [ -r /run/systemd/container ]; then
    cat /run/systemd/container 2>/dev/null | tr -d '\n' || echo container
    return
  fi
  if [ -n "${container:-}" ]; then
    echo "$container"
    return
  fi
  if command -v systemd-detect-virt >/dev/null 2>&1; then
    v=$(systemd-detect-virt 2>/dev/null || true)
    if [ -n "$v" ] && [ "$v" != none ]; then
      echo "$v"
      return
    fi
  fi
  if [ -r /proc/1/cgroup ] && grep -qiE 'lxc|kubepods|containerd|podman|libpod|incus' /proc/1/cgroup 2>/dev/null; then
    echo container
    return
  fi
  echo bare
}

create_system_user() {
  # Portable system user creation across Debian/Ubuntu, Alpine, RHEL-family.
  user=$1
  home=$2
  if id "$user" >/dev/null 2>&1; then
    return 0
  fi
  if command -v useradd >/dev/null 2>&1; then
    # Debian/RHEL/SUSE
    useradd --system --home "$home" --shell /usr/sbin/nologin "$user" 2>/dev/null       || useradd --system --home-dir "$home" --shell /sbin/nologin "$user" 2>/dev/null       || useradd -r -d "$home" -s /sbin/nologin "$user"
  elif command -v adduser >/dev/null 2>&1; then
    # Alpine BusyBox adduser
    adduser -S -H -h "$home" -s /sbin/nologin "$user" 2>/dev/null       || adduser --system --home "$home" --shell /usr/sbin/nologin --no-create-home "$user"
  else
    echo "无法创建系统用户 $user：缺少 useradd/adduser。" >&2
    exit 1
  fi
}

generate_secret() {
  if command -v openssl >/dev/null 2>&1; then
    openssl rand -hex 32
  else
    od -An -N32 -tx1 /dev/urandom | tr -d ' \n'
  fi
}

controller_env_value() {
  local key=$1 value
  value=$(sed -n "s/^${key}=//p" /etc/oboard/controller.env 2>/dev/null | tail -n1)
  value=${value#\"}
  value=${value%\"}
  value=${value#\'}
  value=${value%\'}
  printf '%s\n' "$value"
}

set_controller_env_value() {
  local key=$1 value=$2 env_file=/etc/oboard/controller.env tmp escaped
  # systemd EnvironmentFile understands unquoted or double-quoted values; avoid single quotes.
  escaped=$(printf '%s' "$value" | sed -e 's/\\/\\\\/g' -e 's/"/\\"/g')
  tmp=$(mktemp /etc/oboard/controller.env.XXXXXX)
  sed "/^${key}=/d" "$env_file" > "$tmp"
  printf '%s="%s"\n' "$key" "$escaped" >> "$tmp"
  chmod 0600 "$tmp"
  mv "$tmp" "$env_file"
}

configure_bootstrap_admin() {
  local username password confirm configured_username configured_password
  [ "$CONTROLLER_DATA_EXISTED" = 0 ] || return 0

  configured_username=$(controller_env_value OBOARD_ADMIN_USERNAME)
  configured_password=$(controller_env_value OBOARD_ADMIN_PASSWORD)
  username=${ADMIN_USERNAME_INPUT:-${configured_username:-admin}}
  password=${ADMIN_PASSWORD_INPUT:-$configured_password}

  if [ -r /dev/tty ] && [ -z "$ADMIN_USERNAME_INPUT" ]; then
    printf '\n设置超级管理员\n' > /dev/tty
    printf '该账号会自动加入“管理员组”，并作为首位超级管理员受到保护，不能在面板中删除。\n' > /dev/tty
    printf '超级管理员用户名 [%s]：' "$username" > /dev/tty
    IFS= read -r confirm < /dev/tty || true
    [ -z "$confirm" ] || username=$confirm
  fi
  if [ -z "$username" ]; then
    echo "超级管理员用户名不能为空。" >&2
    exit 1
  fi

  if [ -r /dev/tty ] && [ -z "$ADMIN_PASSWORD_INPUT" ] && [ -z "$configured_password" ]; then
    while :; do
      printf '超级管理员密码（至少 10 位；留空则自动生成一次性随机密码）：' > /dev/tty
      stty -echo < /dev/tty
      IFS= read -r password < /dev/tty || true
      stty echo < /dev/tty
      printf '\n' > /dev/tty
      [ -z "$password" ] && break
      printf '再次输入密码：' > /dev/tty
      stty -echo < /dev/tty
      IFS= read -r confirm < /dev/tty || true
      stty echo < /dev/tty
      printf '\n' > /dev/tty
      if [ "$password" = "$confirm" ]; then
        break
      fi
      printf '两次输入的密码不一致，请重新输入。\n' > /dev/tty
    done
  fi
  if [ -n "$password" ] && [ "${#password}" -lt 10 ]; then
    echo "超级管理员密码至少需要 10 位。" >&2
    exit 1
  fi

  set_controller_env_value OBOARD_ADMIN_USERNAME "$username"
  if [ -n "$password" ]; then
    set_controller_env_value OBOARD_ADMIN_PASSWORD "$password"
    BOOTSTRAP_ADMIN_PASSWORD_CONFIGURED=1
  fi
  BOOTSTRAP_ADMIN_USERNAME=$username
}

prepare_controller_env() {
  local env_file=/etc/oboard/controller.env
  if [ ! -f "$env_file" ]; then
    return 0
  fi
  if grep -q '^OBOARD_SESSION_SECRET=replace-with-at-least-32-random-bytes$' "$env_file"; then
    local secret
    secret=$(generate_secret)
    sed -i "s#^OBOARD_SESSION_SECRET=.*#OBOARD_SESSION_SECRET=$secret#" "$env_file"
    chmod 0600 "$env_file"
  fi
  if [ -n "${OBOARD_BASE_PATH:-}" ]; then
    case "$OBOARD_BASE_PATH" in
      /) OBOARD_BASE_PATH= ;;
      /*) OBOARD_BASE_PATH=${OBOARD_BASE_PATH%/} ;;
      *) echo "OBOARD_BASE_PATH 必须以 / 开头，例如 /abc。" >&2; exit 1 ;;
    esac
    case "$OBOARD_BASE_PATH" in
      *[!A-Za-z0-9/._~-]*|*//*|*/./*|*/../*|*/.|*/..)
        echo "OBOARD_BASE_PATH 只能包含安全的 URL 路径字符，且不能包含空段或点路径。" >&2
        exit 1
        ;;
    esac
    if grep -q '^OBOARD_BASE_PATH=' "$env_file"; then
      sed -i "s#^OBOARD_BASE_PATH=.*#OBOARD_BASE_PATH=${OBOARD_BASE_PATH%/}#" "$env_file"
    else
      printf 'OBOARD_BASE_PATH=%s\n' "${OBOARD_BASE_PATH%/}" >> "$env_file"
    fi
    chmod 0600 "$env_file"
  fi
}

valid_ipv4() {
  local value=$1 old_ifs octet
  case "$value" in ""|*[!0-9.]*) return 1 ;; esac
  old_ifs=$IFS
  IFS=.
  set -- $value
  IFS=$old_ifs
  [ "$#" -eq 4 ] || return 1
  for octet in "$@"; do
    case "$octet" in ""|*[!0-9]*) return 1 ;; esac
    [ "$octet" -le 255 ] || return 1
  done
}

valid_ipv6() {
  local value=$1
  case "$value" in *:*) ;; *) return 1 ;; esac
  case "$value" in *[!0-9A-Fa-f:.]*) return 1 ;; esac
}

is_private_ipv4() {
  local value=$1 old_ifs first second
  valid_ipv4 "$value" || return 1
  old_ifs=$IFS
  IFS=.
  set -- $value
  IFS=$old_ifs
  first=$1
  second=$2
  [ "$first" -eq 10 ] && return 0
  [ "$first" -eq 172 ] && [ "$second" -ge 16 ] && [ "$second" -le 31 ] && return 0
  [ "$first" -eq 192 ] && [ "$second" -eq 168 ] && return 0
  [ "$first" -eq 100 ] && [ "$second" -ge 64 ] && [ "$second" -le 127 ] && return 0
  return 1
}

detect_lan_ip() {
  local configured=${OBOARD_LAN_IP:-} candidate
  if valid_ipv4 "$configured" || valid_ipv6 "$configured"; then
    printf '%s\n' "$configured"
    return 0
  fi
  if command -v ip >/dev/null 2>&1; then
    candidate=$(ip -4 route get 1.1.1.1 2>/dev/null | awk '{for (i=1; i<=NF; i++) if ($i == "src") {print $(i+1); exit}}')
    if is_private_ipv4 "$candidate"; then
      printf '%s\n' "$candidate"
      return 0
    fi
    for candidate in $(ip -o -4 addr show scope global 2>/dev/null | awk '{split($4, address, "/"); print address[1]}'); do
      if is_private_ipv4 "$candidate"; then
        printf '%s\n' "$candidate"
        return 0
      fi
    done
  fi
  if command -v hostname >/dev/null 2>&1; then
    for candidate in $(hostname -I 2>/dev/null || true); do
      if is_private_ipv4 "$candidate"; then
        printf '%s\n' "$candidate"
        return 0
      fi
    done
  fi
  return 1
}

detect_public_ip() {
  local configured=${OBOARD_PUBLIC_IP:-} endpoint candidate
  if valid_ipv4 "$configured" || valid_ipv6 "$configured"; then
    printf '%s\n' "$configured"
    return 0
  fi
  for endpoint in https://api.ipify.org https://ifconfig.me/ip https://icanhazip.com; do
    candidate=$(curl -4 -fsS --connect-timeout 2 --max-time 4 "$endpoint" 2>/dev/null | tr -d '[:space:]' || true)
    if valid_ipv4 "$candidate"; then
      printf '%s\n' "$candidate"
      return 0
    fi
  done
  candidate=$(curl -6 -fsS --connect-timeout 2 --max-time 4 https://api64.ipify.org 2>/dev/null | tr -d '[:space:]' || true)
  if valid_ipv6 "$candidate"; then
    printf '%s\n' "$candidate"
    return 0
  fi
  return 1
}

controller_base_path() {
  local base_path=${OBOARD_BASE_PATH:-}
  if [ -z "$base_path" ]; then
    base_path=$( (sed -n 's/^OBOARD_BASE_PATH=//p' /etc/oboard/controller.env 2>/dev/null || true) | tail -n1)
  fi
  base_path=${base_path%/}
  [ "$base_path" = / ] && base_path=
  printf '%s' "$base_path"
}

controller_port() {
  local addr=${OBOARD_ADDR:-} port
  if [ -z "$addr" ]; then
    addr=$( (sed -n 's/^OBOARD_ADDR=//p' /etc/oboard/controller.env 2>/dev/null || true) | tail -n1)
  fi
  port=${addr##*:}
  case "$port" in *[!0-9]*|"") port=2787 ;; esac
  printf '%s' "$port"
}

controller_url() {
  local host=$1
  case "$host" in *:*) host="[$host]" ;; esac
  printf 'http://%s:%s%s' "$host" "$(controller_port)" "$(controller_base_path)"
}

configured_public_url() {
  local public_url=${OBOARD_PUBLIC_URL%/} base_path
  base_path=$(controller_base_path)
  case "$public_url" in *"$base_path") ;; *) public_url="$public_url$base_path" ;; esac
  printf '%s' "$public_url"
}

controller_agent_url() {
  if [ -n "${OBOARD_PUBLIC_URL:-}" ]; then
    configured_public_url
  else
    controller_url 127.0.0.1
  fi
}

print_controller_urls() {
  local lan_ip public_ip
  if [ -n "${OBOARD_PUBLIC_URL:-}" ]; then
    echo "  访问地址：$(configured_public_url)"
    return
  fi
  lan_ip=$(detect_lan_ip || true)
  public_ip=$(detect_public_ip || true)
  if [ -n "$lan_ip" ]; then
    echo "  内网访问：$(controller_url "$lan_ip")"
  fi
  if [ -n "$public_ip" ] && [ "$public_ip" != "$lan_ip" ]; then
    echo "  公网访问：$(controller_url "$public_ip")"
  fi
  if [ -z "$lan_ip" ] && [ -z "$public_ip" ]; then
    echo "  本机访问：$(controller_url 127.0.0.1)"
    echo "  未能自动探测服务器 IP，请确认网卡和公网出口后替换地址。"
  fi
}

print_controller_help() {
  local url
  url=$(controller_agent_url)
  echo ""
  echo "========================================"
  echo "OBoard 主控安装 / 更新完成"
  echo "========================================"
  echo "面板地址："
  print_controller_urls
  echo "配置文件：/etc/oboard/controller.env"
  echo "数据目录：/var/lib/oboard"
  echo ""
  if [ "$CONTROLLER_DATA_EXISTED" = 1 ]; then
    echo "原有面板账号和数据已保留，请使用原账号登录。"
  else
    echo "超级管理员账号：${BOOTSTRAP_ADMIN_USERNAME:-admin}"
    echo "该账号已自动加入“管理员组”，并作为首位超级管理员不能在面板中删除。"
    if [ "$BOOTSTRAP_ADMIN_PASSWORD_CONFIGURED" = 1 ]; then
      echo "超级管理员密码：已按安装时设置保存，不会输出到服务日志。"
    else
      echo "超级管理员密码：由主控首次启动时生成并打印到服务日志（一次性）。"
      echo "查看方式示例："
      if [ "$SERVICE_MANAGER" = openrc ]; then
        echo "  tail -n 100 /var/log/oboard-controller.log | grep -A2 'first administrator'"
      else
        echo "  journalctl -u oboard-controller -n 100 --no-pager | grep -A2 'first administrator'"
      fi
    fi
    echo "登录后请立即修改密码。"
  fi
  echo ""
  if [ "$SERVICE_MANAGER" = systemd ]; then
    echo "常用维护命令："
    echo "  systemctl status oboard-controller     查看运行状态"
    echo "  systemctl restart oboard-controller    重启主控"
    echo "  journalctl -u oboard-controller -n 100 --no-pager"
  elif [ "$SERVICE_MANAGER" = openrc ]; then
    echo "常用维护命令："
    echo "  rc-service oboard-controller status    查看运行状态"
    echo "  rc-service oboard-controller restart   重启主控"
    echo "  tail -n 100 /var/log/oboard-controller.log"
  else
    echo "未识别服务管理器，请手动启动 oboard-controller。"
  fi
  echo ""
  echo "Controller 和 Agent 相互独立，也可以安装在同一台服务器上。"
  echo "主控服务：oboard-controller；Agent 服务：oboard-agent、oboard-sb。"
  echo "两者使用独立配置和数据目录，不会互相覆盖。"
  echo "如需让本机同时作为节点，请先在面板添加服务器并取得安装令牌，然后执行："
  echo "  curl -fsSL '$url/install/agent.sh' | OBOARD_ENROLL_TOKEN='面板令牌' bash"
  echo "Agent 安装完成后，可在 SSH 中输入 obag 打开管理菜单。"
  echo "========================================"
}

install_agent_from_controller() {
  local action=${OBOARD_AGENT_ACTION:-install}
  local controller_url=${OBOARD_CONTROLLER_URL:-}
  if [ -z "$controller_url" ]; then
    controller_url=$(controller_agent_url)
  fi
  controller_url=${controller_url%/}
  if [ "$action" = install ] && [ -z "${OBOARD_ENROLL_TOKEN:-}" ]; then
    echo "安装 Agent 需要面板生成的一次性安装令牌。" >&2
    echo "请先在 OBoard 面板添加服务器，然后复制该服务器的安装命令。" >&2
    echo "Controller 与 Agent 可以安装在同一台机器上，不会互相覆盖。" >&2
    exit 1
  fi
  echo ""
  echo "OBoard Agent 独立安装"
  echo "===================="
  echo "主控地址：$controller_url"
  if [ "$action" = update ]; then
    curl -fsSL "$controller_url/install/agent.sh" | bash -s -- update
  else
    curl -fsSL "$controller_url/install/agent.sh" | OBOARD_ENROLL_TOKEN="$OBOARD_ENROLL_TOKEN" bash
  fi
}

detect_os() {
  case "$(uname -s | tr '[:upper:]' '[:lower:]')" in
    linux) echo linux ;;
    *) echo "unsupported" ;;
  esac
}

detect_distro() {
  if [ -r /etc/os-release ]; then
    . /etc/os-release
    echo "${ID:-linux}"
  else
    echo linux
  fi
}

detect_service_manager() {
  if command -v systemctl >/dev/null 2>&1 && [ -d /run/systemd/system ]; then
    echo systemd
  elif command -v rc-service >/dev/null 2>&1 || command -v openrc >/dev/null 2>&1 || [ -x /sbin/openrc-run ]; then
    echo openrc
  else
    echo unknown
  fi
}

detect_arch() {
  case "$(uname -m)" in
    x86_64|amd64) echo amd64 ;;
    aarch64|arm64) echo arm64 ;;
    *) echo "unsupported" ;;
  esac
}

latest_version() {
  local payload version
  payload=$(curl --proto '=https' --tlsv1.2 --connect-timeout 10 --max-time 30 -fsSL \
    "https://api.github.com/repos/$REPO/releases/latest" 2>/dev/null || true)
  version=$(printf '%s' "$payload" | sed -n 's/.*"tag_name": *"v\{0,1\}\([^"]*\)".*/\1/p' | head -n 1)
  if [ -n "$version" ]; then
    printf '%s\n' "$version"
    return 0
  fi
  if curl --proto '=https' --tlsv1.2 --connect-timeout 10 --max-time 30 -fsSL --range 0-0 \
    "https://github.com/$REPO/releases/download/dev/sha256sums.txt" -o /dev/null 2>/dev/null; then
    printf 'dev\n'
    return 0
  fi
  return 1
}

download_file() {
  local url=$1 destination=$2 attempt
  for attempt in 1 2 3 4 5; do
    if curl --proto '=https' --tlsv1.2 --connect-timeout 10 --max-time 300 -fsL "$url" -o "$destination"; then
      return 0
    fi
    rm -f "$destination"
    [ "$attempt" = 5 ] || sleep 2
  done
  return 1
}

verify_archive_paths() {
  local archive=$1 listing
  if ! listing=$(tar -tzf "$archive"); then
    echo "安装包损坏或格式不正确，已停止安装。" >&2
    return 1
  fi
  if printf '%s\n' "$listing" | grep -E '(^/|(^|/)\.\.(/|$))' >/dev/null; then
    echo "安装包包含不安全的文件路径，已停止安装。" >&2
    return 1
  fi
}

verify_checksum() {
  local archive=$1 version=$2 name=$3
  local sums="$TMP_DIR/sha256sums.txt"
  local release_tag="v$version"
  [ "$version" = dev ] && release_tag=dev
  local sums_url="https://github.com/$REPO/releases/download/$release_tag/sha256sums.txt"
  if ! download_file "$sums_url" "$sums"; then
    if [ "${OBOARD_SKIP_CHECKSUM:-0}" != "1" ]; then
      echo "无法下载安装包校验文件，已停止安装。" >&2
      echo "请检查服务器是否可以访问 GitHub，然后稍后重试。" >&2
      exit 1
    fi
    echo "未下载到校验文件，已按 OBOARD_SKIP_CHECKSUM=1 跳过校验。" >&2
    return 0
  fi
  if ! awk -v name="$name" '$2 == name || $2 ~ "/" name "$" { print $1 "  " name; found=1 } END { exit found ? 0 : 1 }' \
    "$sums" > "$TMP_DIR/$name.sha256"; then
    echo "校验文件中没有当前安装包，已停止安装。" >&2
    exit 1
  fi
  if command -v sha256sum >/dev/null 2>&1; then
    if ! (cd "$TMP_DIR" && sha256sum -c "$name.sha256"); then
      echo "安装包校验失败，请重新下载安装。" >&2
      exit 1
    fi
  elif command -v shasum >/dev/null 2>&1; then
    if ! (cd "$TMP_DIR" && shasum -a 256 -c "$name.sha256"); then
      echo "安装包校验失败，请重新下载安装。" >&2
      exit 1
    fi
  else
    echo "缺少安装包校验工具，请安装 sha256sum 或 shasum 后重试。" >&2
    exit 1
  fi
}


wait_for_controller_updater() {
  local i
  for i in 1 2 3 4 5 6 7 8 9 10; do
    if [ -S /run/oboard/controller-updater.sock ] &&
      curl --unix-socket /run/oboard/controller-updater.sock --max-time 2 --fail --silent --show-error \
        http://localhost/v1/status >/dev/null 2>&1; then
      return 0
    fi
    sleep 1
  done
  echo "主控更新器未就绪：无法通过 /run/oboard/controller-updater.sock 访问状态接口。" >&2
  if command -v systemctl >/dev/null 2>&1; then
    systemctl --no-pager --full status oboard-controller-updater >&2 || true
    journalctl -u oboard-controller-updater -n 40 --no-pager >&2 || true
  elif command -v rc-service >/dev/null 2>&1; then
    rc-service oboard-controller-updater status >&2 || true
    [ ! -f /var/log/oboard-controller-updater.log ] || tail -n 40 /var/log/oboard-controller-updater.log >&2 || true
  fi
  return 1
}

prepare_controller_updater_runtime() {
  install -d -m 0750 -o root -g oboard /run/oboard
  if [ -L /var/lib/oboard ]; then
    echo "拒绝使用符号链接形式的更新器状态目录：/var/lib/oboard" >&2
    return 1
  fi
  if [ -e /var/lib/oboard ] && [ ! -d /var/lib/oboard ]; then
    echo "更新器状态路径不是目录：/var/lib/oboard" >&2
    return 1
  fi
  if [ ! -d /var/lib/oboard ]; then
    install -d -m 0750 -o root -g root /var/lib/oboard
  fi
  install -d -m 0700 -o root -g root /var/lib/oboard/controller-update
}

start_controller_systemd() {
  if [ "${OBOARD_START_SERVICE:-1}" = "0" ]; then
    echo "已按 OBOARD_START_SERVICE=0 跳过主控启动。" >&2
    return 0
  fi
  systemctl restart oboard-controller
  echo "主控服务已启动。"
}

start_controller_openrc() {
  if [ "${OBOARD_START_SERVICE:-1}" = "0" ]; then
    echo "已按 OBOARD_START_SERVICE=0 跳过主控启动。" >&2
    return 0
  fi
  rc-service oboard-controller restart
  echo "主控服务已启动。"
}

uninstall_controller() {
  local service_manager=$1 purge=${OBOARD_PURGE_DATA:-0}
  case "$purge" in
    0|1) ;;
    *)
      echo "OBOARD_PURGE_DATA 只能设置为 0 或 1。" >&2
      return 1
      ;;
  esac
  if [ "$INSTALLATION_EXISTS" != 1 ]; then
    echo "未检测到已安装的 OBoard 主控，无需卸载。"
    return 0
  fi

  echo "正在卸载 OBoard 主控..."
  case "$service_manager" in
    systemd)
      systemctl disable --now oboard-controller.service >/dev/null 2>&1 || true
      systemctl disable --now oboard-controller-updater.service >/dev/null 2>&1 || true
      ;;
    openrc)
      rc-service oboard-controller stop >/dev/null 2>&1 || true
      rc-service oboard-controller-updater stop >/dev/null 2>&1 || true
      rc-update del oboard-controller default >/dev/null 2>&1 || true
      rc-update del oboard-controller-updater default >/dev/null 2>&1 || true
      ;;
  esac

  rm -f /etc/systemd/system/oboard-controller.service \
    /etc/systemd/system/oboard-controller-updater.service \
    /etc/init.d/oboard-controller \
    /etc/init.d/oboard-controller-updater
  if [ "$service_manager" = systemd ]; then
    systemctl daemon-reload
    systemctl reset-failed oboard-controller.service oboard-controller-updater.service >/dev/null 2>&1 || true
  fi
  rm -f "$INSTALL_DIR/oboard-controller" \
    "$INSTALL_DIR/oboard-controller-updater" \
    "$INSTALL_DIR/oboard-controller.update-backup" \
    "$INSTALL_DIR/oboard-controller.update-new" \
    "$INSTALL_DIR/oboard-controller-updater.update-backup" \
    "$INSTALL_DIR/oboard-controller-updater.update-new"
  rm -rf /opt/oboard/web /opt/oboard/downloads /run/oboard /var/lib/oboard/controller-update
  rmdir /opt/oboard 2>/dev/null || true
  rm -f /var/log/oboard-controller.log /var/log/oboard-controller-updater.log

  if [ "$purge" = 1 ]; then
    rm -rf /etc/oboard /var/lib/oboard
    if command -v userdel >/dev/null 2>&1; then
      userdel oboard >/dev/null 2>&1 || true
    elif command -v deluser >/dev/null 2>&1; then
      deluser oboard >/dev/null 2>&1 || true
    fi
    if command -v groupdel >/dev/null 2>&1; then
      groupdel oboard >/dev/null 2>&1 || true
    elif command -v delgroup >/dev/null 2>&1; then
      delgroup oboard >/dev/null 2>&1 || true
    fi
    echo "OBoard 主控已卸载，配置和数据已删除。"
  else
    echo "OBoard 主控已卸载。"
    echo "配置和数据已保留在 /etc/oboard 和 /var/lib/oboard。"
    echo "再次安装时会自动使用原有账号和数据。"
    echo "如需彻底删除，请重新执行卸载命令并添加 OBOARD_PURGE_DATA=1。"
  fi
}

install_file_atomic() {
  local source=$1 destination=$2 mode=${3:-0755}
  install -m "$mode" "$source" "$destination.new"
  mv -f "$destination.new" "$destination"
}

replace_tree_atomic() {
  local source=$1 destination=$2 owner=${3:-}
  rm -rf "$destination.new" "$destination.old"
  cp -R "$source" "$destination.new"
  if [ -n "$owner" ]; then
    chown -R "$owner" "$destination.new"
  fi
  if [ -e "$destination" ]; then
    mv "$destination" "$destination.old"
  fi
  if ! mv "$destination.new" "$destination"; then
    [ ! -e "$destination.old" ] || mv "$destination.old" "$destination"
    return 1
  fi
  rm -rf "$destination.old"
}

install_component() {
  local component=$1 os=$2 arch=$3 version=$4
  local service_manager=${5:-unknown}
  local artifact_version=$version release_tag="v$version"
  if [ "$version" = dev ]; then
    artifact_version=dev
    release_tag=dev
  fi
  local archive="oboard_${component}_${artifact_version}_${os}_${arch}.tar.gz"
  if [ "$component" = controller ]; then
    archive="oboard_controller_${artifact_version}_${os}_${arch}_install.tar.gz"
  fi
  local url="https://github.com/$REPO/releases/download/$release_tag/$archive"
  local work="$TMP_DIR/$component"

  echo ""
  echo "[1/4] 下载 OBoard $component $version"
  mkdir -p "$work"
  if ! download_file "$url" "$TMP_DIR/$archive"; then
    echo "安装包下载失败：$archive" >&2
    echo "请检查服务器是否可以访问 GitHub，然后稍后重试。" >&2
    return 1
  fi
  echo "[2/4] 校验安装包"
  verify_checksum "$TMP_DIR/$archive" "$version" "$archive"
  verify_archive_paths "$TMP_DIR/$archive"
  tar -xzf "$TMP_DIR/$archive" -C "$work"
  echo "[3/4] 安装程序文件"
  install_file_atomic "$work/bin/oboard-$component" "$INSTALL_DIR/oboard-$component" 0755
  if [ "$component" = controller ]; then
    install_file_atomic "$work/bin/oboard-controller-updater" "$INSTALL_DIR/oboard-controller-updater" 0755
  fi

  if [ "$os" = linux ] && [ "$service_manager" = systemd ] && [ -d "$work/deploy/systemd" ]; then
    case "$component" in
      controller)
        create_system_user oboard /var/lib/oboard
        install -d -m 0750 -o oboard -g oboard /var/lib/oboard /var/lib/oboard/backups /opt/oboard/web /opt/oboard/downloads /etc/oboard
        replace_tree_atomic "$work/web/dist" /opt/oboard/web/dist oboard:oboard
        if [ -d "$work/downloads" ]; then
          replace_tree_atomic "$work/downloads" /opt/oboard/downloads oboard:oboard
        fi
        if [ ! -f /etc/oboard/controller.env ]; then
          cp "$work/deploy/controller.env.example" /etc/oboard/controller.env
          chmod 0600 /etc/oboard/controller.env
        fi
        prepare_controller_env
        set_controller_env_value OBOARD_UPDATE_CHANNEL "$INSTALL_CHANNEL"
        configure_bootstrap_admin
        cp "$work/deploy/systemd/oboard-controller.service" /etc/systemd/system/
        cp "$work/deploy/systemd/oboard-controller-updater.service" /etc/systemd/system/
        prepare_controller_updater_runtime
        systemctl daemon-reload
        systemctl enable oboard-controller-updater
        systemctl restart oboard-controller-updater
        wait_for_controller_updater
        systemctl enable oboard-controller
        start_controller_systemd
        ;;
      agent)
        install -d -m 0700 /etc/oboard-agent /var/lib/oboard-agent
        cp "$work/deploy/systemd/oboard-agent.service" /etc/systemd/system/
        systemctl daemon-reload
        systemctl enable oboard-agent
        ;;
      sb)
        cp "$work/deploy/systemd/oboard-sb.service" /etc/systemd/system/
        systemctl daemon-reload
        systemctl enable oboard-sb
        ;;
    esac
  elif [ "$os" = linux ] && [ "$service_manager" = openrc ] && [ -d "$work/deploy/openrc" ]; then
    case "$component" in
      controller)
        create_system_user oboard /var/lib/oboard
        install -d -m 0750 -o oboard -g oboard /var/lib/oboard /var/lib/oboard/backups /opt/oboard/web /opt/oboard/downloads /etc/oboard
        replace_tree_atomic "$work/web/dist" /opt/oboard/web/dist oboard:oboard
        if [ -d "$work/downloads" ]; then
          replace_tree_atomic "$work/downloads" /opt/oboard/downloads oboard:oboard
        fi
        if [ ! -f /etc/oboard/controller.env ]; then
          cp "$work/deploy/controller.env.example" /etc/oboard/controller.env
          chmod 0600 /etc/oboard/controller.env
        fi
        prepare_controller_env
        set_controller_env_value OBOARD_UPDATE_CHANNEL "$INSTALL_CHANNEL"
        configure_bootstrap_admin
        cp "$work/deploy/openrc/oboard-controller" /etc/init.d/oboard-controller
        cp "$work/deploy/openrc/oboard-controller-updater" /etc/init.d/oboard-controller-updater
        chmod 0755 /etc/init.d/oboard-controller
        chmod 0755 /etc/init.d/oboard-controller-updater
        prepare_controller_updater_runtime
        rc-update add oboard-controller-updater default
        rc-service oboard-controller-updater restart
        wait_for_controller_updater
        rc-update add oboard-controller default
        start_controller_openrc
        ;;
      agent)
        install -d -m 0700 /etc/oboard-agent /var/lib/oboard-agent
        cp "$work/deploy/openrc/oboard-agent" /etc/init.d/oboard-agent
        chmod 0755 /etc/init.d/oboard-agent
        rc-update add oboard-agent default
        ;;
      sb)
        install -d -m 0700 /var/lib/oboard-agent
        cp "$work/deploy/openrc/oboard-sb" /etc/init.d/oboard-sb
        chmod 0755 /etc/init.d/oboard-sb
        rc-update add oboard-sb default
        ;;
    esac
  elif [ "$os" = linux ]; then
    echo "==> Installed binary only: no supported service manager detected. Debian/Ubuntu use systemd; Alpine uses OpenRC." >&2
  fi
}

case "$COMPONENT" in
  controller|controller-agent|all) select_installation ;;
esac
need_root
if [ "$ACTION" = uninstall ]; then
  SERVICE_MANAGER=$(detect_service_manager)
  uninstall_controller "$SERVICE_MANAGER"
  drain_piped_script
  exit 0
fi
if [ "$ACTION" = update ] && [ -z "$VERSION_INPUT" ]; then
  installed_channel=$(sed -n 's/^OBOARD_UPDATE_CHANNEL=//p' /etc/oboard/controller.env 2>/dev/null | tail -n1 | tr -d "'\"")
  case "$installed_channel" in
    dev) VERSION_VALUE=dev ;;
    pinned)
      echo "当前主控使用固定版本。请设置 VERSION=latest 或 VERSION=dev 后再更新。" >&2
      exit 1
      ;;
    *) VERSION_VALUE=latest ;;
  esac
fi

TMP_DIR=$(make_install_tmp)
trap cleanup EXIT

echo "OBoard 安装程序"
echo "==============="
case "$COMPONENT" in
  controller|controller-agent|all)
    if [ "$ACTION" = update ]; then
      echo "检测到已安装的二进制主控，将保留现有数据和设置并更新。"
    else
      echo "正在安装 OBoard 主控。"
    fi
    ;;
esac
OS_VALUE=$(detect_os)
ARCH_VALUE=$(detect_arch)
DISTRO_VALUE=$(detect_distro)
SERVICE_MANAGER=$(detect_service_manager)
VIRT_HINT=$(detect_virt_hint)
echo "系统：$DISTRO_VALUE / $ARCH_VALUE / $SERVICE_MANAGER / virt=$VIRT_HINT"
if [ "$OS_VALUE" = unsupported ] || [ "$ARCH_VALUE" = unsupported ]; then
  echo "Unsupported platform: $(uname -s)/$(uname -m)" >&2
  exit 1
fi
if [ "$OS_VALUE" != linux ]; then
  echo "OBoard production packages support Linux only. Detected: $(uname -s)" >&2
  exit 1
fi
case "$DISTRO_VALUE" in
  debian|ubuntu|alpine|centos|rhel|rocky|almalinux|fedora|amzn|ol|opensuse*|sles|arch|manjaro|linux) ;;
  *) echo "==> Distro $DISTRO_VALUE is not a primary target; continuing with generic Linux service detection." >&2 ;;
esac
ensure_base_tools

case "$COMPONENT" in
  agent|agent-sb|node)
    install_agent_from_controller
    exit 0
    ;;
esac

case "$VERSION_VALUE" in
  latest|stable|"")
    VERSION_VALUE=$(latest_version || true)
    if [ "$VERSION_VALUE" = dev ]; then
      INSTALL_CHANNEL=dev
      echo "当前暂无可用的稳定版，将安装最新开发版。"
    else
      INSTALL_CHANNEL=stable
    fi
    ;;
  dev|development|nightly) VERSION_VALUE=dev; INSTALL_CHANNEL=dev ;;
  *) INSTALL_CHANNEL=pinned ;;
esac
if [ -z "$VERSION_VALUE" ]; then
  echo "无法获取可用的主控版本。请检查服务器是否可以访问 GitHub，然后重试。" >&2
  exit 1
fi
VERSION_VALUE=${VERSION_VALUE#v}
echo "目标：$COMPONENT $VERSION_VALUE"

case "$COMPONENT" in
  controller)
    ensure_acme_sh
    install_component controller "$OS_VALUE" "$ARCH_VALUE" "$VERSION_VALUE" "$SERVICE_MANAGER"
    ;;
  controller-agent|all)
    ensure_acme_sh
    install_component controller "$OS_VALUE" "$ARCH_VALUE" "$VERSION_VALUE" "$SERVICE_MANAGER"
    if [ -n "${OBOARD_ENROLL_TOKEN:-}" ]; then
      install_agent_from_controller
    else
      echo ""
      echo "主控已安装。登录面板添加本机服务器并取得安装令牌后，再执行 COMPONENT=agent 安装 Agent。"
    fi
    ;;
  *)
    echo "用法：" >&2
    echo "  安装主控：COMPONENT=controller $0" >&2
    echo "  安装 Agent：COMPONENT=agent OBOARD_CONTROLLER_URL=主控地址 OBOARD_ENROLL_TOKEN=面板令牌 $0" >&2
    echo "  同机安装：COMPONENT=controller-agent OBOARD_ENROLL_TOKEN=面板令牌 $0" >&2
    exit 1
    ;;
esac

echo "[4/4] OBoard $COMPONENT $VERSION_VALUE 已安装"
case "$COMPONENT" in
  controller|controller-agent|all) print_controller_help ;;
esac

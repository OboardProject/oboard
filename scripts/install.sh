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
COMPONENT=${COMPONENT:-${1:-controller}}
INSTALL_DIR=${INSTALL_DIR:-/usr/local/bin}
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

if [ "${OBOARD_INSTALL_METHOD:-binary}" = docker ]; then
  docker_script=$(CDPATH= cd -- "$(dirname -- "$0")" 2>/dev/null && pwd)/install-docker.sh
  if [ -f "$docker_script" ]; then
    if [ "${OBOARD_ACTION:-install}" = update ] && [ -z "$VERSION_INPUT" ]; then
      exec env OBOARD_ACTION=update sh "$docker_script"
    fi
    exec env OBOARD_ACTION="${OBOARD_ACTION:-install}" VERSION="$VERSION_VALUE" sh "$docker_script"
  fi
  if [ "${OBOARD_ACTION:-install}" = update ] && [ -z "$VERSION_INPUT" ]; then
    curl --proto '=https' --tlsv1.2 -fsSL "https://raw.githubusercontent.com/$REPO/main/scripts/install-docker.sh" | env OBOARD_ACTION=update sh
    exit $?
  fi
  curl --proto '=https' --tlsv1.2 -fsSL "https://raw.githubusercontent.com/$REPO/main/scripts/install-docker.sh" | env OBOARD_ACTION="${OBOARD_ACTION:-install}" VERSION="$VERSION_VALUE" sh
  exit $?
fi

TMP_DIR=$(make_install_tmp)

cleanup() { [ -z "$TMP_DIR" ] || rm -rf "$TMP_DIR"; }
trap cleanup EXIT

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
  echo "正在安装安装依赖（curl / CA / tar / checksum）..."
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
  if [ -f /.dockerenv ] || [ -f /run/.containerenv ]; then
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
  if [ -r /proc/1/cgroup ] && grep -qiE 'lxc|docker|kubepods|containerd|podman|libpod|incus' /proc/1/cgroup 2>/dev/null; then
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
  local key=$1
  sed -n "s/^${key}=//p" /etc/oboard/controller.env 2>/dev/null | tail -n1
}

set_controller_env_value() {
  local key=$1 value=$2 env_file=/etc/oboard/controller.env tmp escaped
  escaped=$(printf '%s' "$value" | sed "s/'/'\\\\''/g")
  tmp=$(mktemp /etc/oboard/controller.env.XXXXXX)
  sed "/^${key}=/d" "$env_file" > "$tmp"
  printf "%s='%s'\n" "$key" "$escaped" >> "$tmp"
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

controller_public_url() {
  local addr port host base_path public_url
  base_path=$( (sed -n 's/^OBOARD_BASE_PATH=//p' /etc/oboard/controller.env 2>/dev/null || true) | tail -n1)
  base_path=${base_path%/}
  [ "$base_path" = / ] && base_path=
  if [ -n "${OBOARD_PUBLIC_URL:-}" ]; then
    public_url=${OBOARD_PUBLIC_URL%/}
    case "$public_url" in
      *"$base_path") ;;
      *) public_url="$public_url$base_path" ;;
    esac
    printf '%s\n' "$public_url"
    return
  fi
  addr=$( (sed -n 's/^OBOARD_ADDR=//p' /etc/oboard/controller.env 2>/dev/null || true) | tail -n1)
  port=${addr##*:}
  case "$port" in (*[!0-9]*|"") port=2787 ;; esac
  host=$( (hostname -I 2>/dev/null || true) | awk '{print $1}')
  if [ -z "$host" ] && command -v ip >/dev/null 2>&1; then
    host=$( (ip route get 1.1.1.1 2>/dev/null || true) | awk '{for(i=1;i<=NF;i++) if($i=="src") {print $(i+1); exit}}')
  fi
  [ -n "$host" ] || host=127.0.0.1
  case "$host" in (*:*) host="[$host]" ;; esac
  printf 'http://%s:%s%s\n' "$host" "$port" "$base_path"
}

print_controller_help() {
  local url
  url=$(controller_public_url)
  echo ""
  echo "========================================"
  echo "OBoard 主控安装 / 更新完成"
  echo "========================================"
  echo "面板地址：$url"
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
    controller_url=$(controller_public_url)
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
  curl --proto '=https' --tlsv1.2 -fsSL "https://api.github.com/repos/$REPO/releases/latest" \
    | sed -n 's/.*"tag_name": *"v\{0,1\}\([^"]*\)".*/\1/p' \
    | head -n 1
}

verify_archive_paths() {
  local archive=$1
  if tar -tzf "$archive" | grep -E '(^/|(^|/)\.\.(/|$))' >/dev/null; then
    echo "Archive contains unsafe paths: $archive" >&2
    exit 1
  fi
}

verify_checksum() {
  local archive=$1 version=$2 name=$3
  local sums="$TMP_DIR/sha256sums.txt"
  local sums_url="https://github.com/$REPO/releases/download/v$version/sha256sums.txt"
  if ! curl --proto '=https' --tlsv1.2 -fsSL "$sums_url" -o "$sums"; then
    echo "sha256sums.txt not found for v$version; refusing unchecked install. Set OBOARD_SKIP_CHECKSUM=1 to override." >&2
    if [ "${OBOARD_SKIP_CHECKSUM:-0}" != "1" ]; then
      exit 1
    fi
    return 0
  fi
  awk -v name="$name" '$2 == name || $2 ~ "/" name "$" { print $1 "  " name; found=1 } END { exit found ? 0 : 1 }' "$sums" > "$TMP_DIR/$name.sha256"
  if command -v sha256sum >/dev/null 2>&1; then
    (cd "$TMP_DIR" && sha256sum -c "$name.sha256")
  elif command -v shasum >/dev/null 2>&1; then
    (cd "$TMP_DIR" && shasum -a 256 -c "$name.sha256")
  else
    echo "sha256sum/shasum is required to verify release archives" >&2
    exit 1
  fi
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

install_component() {
  local component=$1 os=$2 arch=$3 version=$4
  local service_manager=${5:-unknown}
  local archive="oboard_${component}_${version}_${os}_${arch}.tar.gz"
  local url="https://github.com/$REPO/releases/download/v$version/$archive"
  local work="$TMP_DIR/$component"

  echo ""
  echo "[1/4] 下载 OBoard $component $version"
  mkdir -p "$work"
  curl --proto '=https' --tlsv1.2 -fL "$url" -o "$TMP_DIR/$archive"
  echo "[2/4] 校验安装包"
  verify_checksum "$TMP_DIR/$archive" "$version" "$archive"
  verify_archive_paths "$TMP_DIR/$archive"
  tar -xzf "$TMP_DIR/$archive" -C "$work"
  echo "[3/4] 安装程序文件"
  install -m 0755 "$work/bin/oboard-$component" "$INSTALL_DIR/oboard-$component"

  if [ "$os" = linux ] && [ "$service_manager" = systemd ] && [ -d "$work/deploy/systemd" ]; then
    case "$component" in
      controller)
        create_system_user oboard /var/lib/oboard
        install -d -m 0750 -o oboard -g oboard /var/lib/oboard /opt/oboard/web /opt/oboard/downloads /etc/oboard
        cp -R "$work/web/dist" /opt/oboard/web/
        if [ -d "$work/downloads" ]; then
          rm -rf /opt/oboard/downloads/*
          cp -R "$work/downloads"/. /opt/oboard/downloads/
          chown -R oboard:oboard /opt/oboard/downloads
        fi
        if [ ! -f /etc/oboard/controller.env ]; then
          cp "$work/deploy/controller.env.example" /etc/oboard/controller.env
          chmod 0600 /etc/oboard/controller.env
        fi
        prepare_controller_env
        configure_bootstrap_admin
        cp "$work/deploy/systemd/oboard-controller.service" /etc/systemd/system/
        systemctl daemon-reload
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
        install -d -m 0750 -o oboard -g oboard /var/lib/oboard /opt/oboard/web /opt/oboard/downloads /etc/oboard
        cp -R "$work/web/dist" /opt/oboard/web/
        if [ -d "$work/downloads" ]; then
          rm -rf /opt/oboard/downloads/*
          cp -R "$work/downloads"/. /opt/oboard/downloads/
          chown -R oboard:oboard /opt/oboard/downloads
        fi
        if [ ! -f /etc/oboard/controller.env ]; then
          cp "$work/deploy/controller.env.example" /etc/oboard/controller.env
          chmod 0600 /etc/oboard/controller.env
        fi
        prepare_controller_env
        configure_bootstrap_admin
        cp "$work/deploy/openrc/oboard-controller" /etc/init.d/oboard-controller
        chmod 0755 /etc/init.d/oboard-controller
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

need_root
echo "OBoard 安装程序"
echo "==============="
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

if [ "$VERSION_VALUE" = latest ]; then
  VERSION_VALUE=$(latest_version)
fi
if [ -z "$VERSION_VALUE" ]; then
  echo "无法获取主控 release 版本。" >&2
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

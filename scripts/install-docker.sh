#!/bin/sh
set -eu

ACTION=${OBOARD_ACTION:-install}
INSTALL_ROOT=${OBOARD_DOCKER_DIR:-/opt/oboard-docker}
IMAGE_INPUT=${OBOARD_IMAGE:-}
VERSION_INPUT=${VERSION:-${OBOARD_VERSION:-}}
PORT_INPUT=${OBOARD_PORT:-}
BASE_PATH_INPUT=${OBOARD_BASE_PATH:-}
TIMEZONE=${TZ:-Asia/Shanghai}
ENV_FILE="$INSTALL_ROOT/.env"
FRESH_INSTALL=1
ADMIN_USERNAME_INPUT=${OBOARD_ADMIN_USERNAME:-}
ADMIN_PASSWORD_INPUT=${OBOARD_ADMIN_PASSWORD:-}
ADMIN_USERNAME=${ADMIN_USERNAME_INPUT:-admin}
ADMIN_PASSWORD=${ADMIN_PASSWORD_INPUT:-}
[ -s "$INSTALL_ROOT/data/oboard.sqlite" ] && FRESH_INSTALL=0

env_value() {
  key=$1
  [ -f "$ENV_FILE" ] || return 0
  sed -n "s/^${key}=//p" "$ENV_FILE" | tail -n1
}

IMAGE=${IMAGE_INPUT:-$(env_value OBOARD_IMAGE)}
[ -n "$IMAGE" ] || IMAGE=ghcr.io/oboardproject/oboard
VERSION_VALUE=${VERSION_INPUT:-$(env_value OBOARD_TAG)}
[ -n "$VERSION_VALUE" ] || VERSION_VALUE=latest
PORT=${PORT_INPUT:-$(env_value OBOARD_PORT)}
[ -n "$PORT" ] || PORT=2787
BASE_PATH=${BASE_PATH_INPUT:-$(env_value OBOARD_BASE_PATH)}

case "$BASE_PATH" in
  ""|/) BASE_PATH= ;;
  /*) BASE_PATH=${BASE_PATH%/} ;;
  *) echo "OBOARD_BASE_PATH 必须以 / 开头，例如 /abc。" >&2; exit 1 ;;
esac
case "$BASE_PATH" in
  *[!A-Za-z0-9/._~-]*|*//*|*/./*|*/../*|*/.|*/..)
    echo "OBOARD_BASE_PATH 只能包含安全的 URL 路径字符，且不能包含空段或点路径。" >&2
    exit 1
    ;;
esac

case "$PORT" in
  *[!0-9]*|"")
    echo "OBOARD_PORT 必须是 1 到 65535 之间的端口号。" >&2
    exit 1
    ;;
esac
if [ "$PORT" -lt 1 ] || [ "$PORT" -gt 65535 ]; then
  echo "OBOARD_PORT 必须是 1 到 65535 之间的端口号。" >&2
  exit 1
fi

# Restrict image/tag characters used in compose interpolation and shell expansion.
case "$IMAGE" in
  ""|*[!A-Za-z0-9._/:@-]*|*..*)
    echo "OBOARD_IMAGE must look like a docker image reference (registry/name)." >&2
    exit 1
    ;;
esac
case "$VERSION_VALUE" in
  ""|*[!A-Za-z0-9._-]*|*..*)
    echo "VERSION/OBOARD_TAG contains invalid characters." >&2
    exit 1
    ;;
esac

need_root() {
  if [ "$(id -u)" -ne 0 ]; then
    echo "Docker 安装需要 root 权限，请使用 sudo 重新执行。" >&2
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
  command -v sha256sum >/dev/null 2>&1 || need_sha=1
  if [ ! -f /etc/ssl/certs/ca-certificates.crt ] && [ ! -f /etc/pki/tls/certs/ca-bundle.crt ] && [ ! -f /etc/ssl/cert.pem ]; then
    need_ca=1
  fi
  if [ "$need_curl" = 0 ] && [ "$need_ca" = 0 ] && [ "$need_tar" = 0 ] && [ "$need_sha" = 0 ]; then
    return 0
  fi
  echo "正在安装基础依赖（curl / CA 证书）..."
  packages=""
  [ "$need_curl" = 1 ] && packages="$packages curl"
  [ "$need_ca" = 1 ] && packages="$packages ca-certificates"
  [ "$need_tar" = 1 ] && packages="$packages tar"
  [ "$need_sha" = 1 ] && packages="$packages coreutils"
  # shellcheck disable=SC2086
  pkg_install $packages || {
    echo "基础依赖安装失败，请手动安装 curl 与 CA 证书后重试。" >&2
    exit 1
  }
  if command -v update-ca-certificates >/dev/null 2>&1; then
    update-ca-certificates >/dev/null 2>&1 || true
  elif command -v update-ca-trust >/dev/null 2>&1; then
    update-ca-trust extract >/dev/null 2>&1 || true
  fi
}

apt_package_available() {
  pkg=$1
  apt-cache show "$pkg" >/dev/null 2>&1
}

apt_install_first_available() {
  for pkg in "$@"; do
    if ! apt_package_available "$pkg"; then
      continue
    fi
    if apt-get install -y --no-install-recommends "$pkg"; then
      printf '%s\n' "$pkg"
      return 0
    fi
  done
  return 1
}

docker_compose_ready() {
  command -v docker >/dev/null 2>&1 && docker compose version >/dev/null 2>&1
}

install_docker_engine() {
  if docker_compose_ready; then
    return
  fi
  if command -v docker >/dev/null 2>&1; then
    echo "已检测到 Docker，但缺少 Compose v2（docker compose），正在补齐..."
  else
    echo "未检测到 Docker，正在安装..."
  fi
  if command -v apt-get >/dev/null 2>&1; then
    # Debian/Ubuntu package names differ:
    # - engine: docker.io (daemon only on Debian Trixie+)
    # - CLI: docker-cli (provides /usr/bin/docker; docker.io only recommends it)
    # - compose v2: docker-compose (Debian Trixie+), docker-compose-v2 (Ubuntu 24.04+),
    #   docker-compose-plugin (Docker CE / third-party repos)
    export DEBIAN_FRONTEND=noninteractive
    apt-get update -y
    if ! command -v docker >/dev/null 2>&1; then
      # --no-install-recommends skips docker-cli on modern Debian, so install it explicitly.
      apt-get install -y --no-install-recommends docker.io docker-cli || {
        # Older Ubuntu/Debian may still ship the CLI inside docker.io only.
        apt-get install -y --no-install-recommends docker.io || {
          echo "无法从当前 apt 源安装 docker.io/docker-cli，请检查包源后重试。" >&2
          exit 1
        }
      }
    fi
    if ! command -v docker >/dev/null 2>&1; then
      if ! apt_install_first_available docker-cli >/dev/null; then
        echo "Docker 守护进程已安装，但缺少 /usr/bin/docker（docker-cli）。" >&2
        echo "请安装 docker-cli 后重试。" >&2
        exit 1
      fi
    fi
    if ! docker_compose_ready; then
      if ! apt_install_first_available docker-compose-plugin docker-compose-v2 docker-compose >/dev/null; then
        echo "无法从当前 apt 源安装 Docker Compose v2 插件。" >&2
        echo "请安装提供 docker compose 的包（Debian: docker-compose，Ubuntu 24.04+: docker-compose-v2）。" >&2
        exit 1
      fi
    fi
  elif command -v apk >/dev/null 2>&1; then
    if ! command -v docker >/dev/null 2>&1; then
      apk add --no-cache docker
    fi
    if ! docker_compose_ready; then
      apk add --no-cache docker-cli-compose || apk add --no-cache docker-compose
    fi
  elif command -v dnf >/dev/null 2>&1; then
    if ! command -v docker >/dev/null 2>&1; then
      dnf install -y docker || dnf install -y moby-engine
    fi
    if ! docker_compose_ready; then
      dnf install -y docker-compose-plugin || dnf install -y docker-compose
    fi
  elif command -v yum >/dev/null 2>&1; then
    if ! command -v docker >/dev/null 2>&1; then
      yum install -y docker || yum install -y moby-engine
    fi
    if ! docker_compose_ready; then
      yum install -y docker-compose-plugin || yum install -y docker-compose
    fi
  elif command -v zypper >/dev/null 2>&1; then
    if ! command -v docker >/dev/null 2>&1; then
      zypper --non-interactive install -y docker
    fi
    if ! docker_compose_ready; then
      zypper --non-interactive install -y docker-compose
    fi
  elif command -v pacman >/dev/null 2>&1; then
    if ! command -v docker >/dev/null 2>&1; then
      pacman -Sy --noconfirm docker
    fi
    if ! docker_compose_ready; then
      pacman -Sy --noconfirm docker-compose
    fi
  else
    echo "未找到支持的包管理器，请先安装 Docker Engine 和 Docker Compose v2。" >&2
    echo "支持：Debian/Ubuntu(apt)、Alpine(apk)、RHEL/CentOS(dnf/yum)、openSUSE(zypper)、Arch(pacman)。" >&2
    exit 1
  fi
  if ! command -v docker >/dev/null 2>&1; then
    echo "Docker 安装后仍不可用，请检查包源后重试。" >&2
    exit 1
  fi
  if ! docker_compose_ready; then
    echo "Docker Compose v2（docker compose）安装后仍不可用，请检查包源后重试。" >&2
    exit 1
  fi
}

start_docker_engine() {
  if command -v systemctl >/dev/null 2>&1 && [ -d /run/systemd/system ]; then
    systemctl enable --now docker >/dev/null 2>&1 || systemctl start docker >/dev/null 2>&1 || true
  elif command -v rc-service >/dev/null 2>&1 || [ -x /sbin/openrc-run ]; then
    rc-update add docker default >/dev/null 2>&1 || true
    rc-service docker start >/dev/null 2>&1 || true
  fi
  # Nested LXC/container hosts may not be able to run a Docker daemon; surface a clear error.
  if ! docker info >/dev/null 2>&1; then
    echo "Docker 服务未正常运行。" >&2
    echo "若在 LXC/容器内安装，请确认宿主已开启嵌套虚拟化/privileged 或改用二进制安装脚本。" >&2
    exit 1
  fi
  if docker compose version >/dev/null 2>&1; then
    return 0
  fi
  echo "缺少 Docker Compose v2（docker compose），请安装 Compose 插件后重试。" >&2
  exit 1
}

compose() {
  docker compose --project-directory "$INSTALL_ROOT" --env-file "$INSTALL_ROOT/.env" -f "$INSTALL_ROOT/docker-compose.yml" "$@"
}

detect_arch() {
  case "$(uname -m)" in
    x86_64|amd64) echo amd64 ;;
    aarch64|arm64) echo arm64 ;;
    *) echo "当前架构没有主控更新器：$(uname -m)" >&2; exit 1 ;;
  esac
}

latest_version() {
  curl -fsSL "https://api.github.com/repos/OboardProject/oboard/releases/latest" \
    | sed -n 's/.*"tag_name": *"v\{0,1\}\([^"]*\)".*/\1/p' | head -n1
}

ensure_oboard_group() {
  group_entry=$(getent group oboard 2>/dev/null || sed -n '/^oboard:/p' /etc/group | head -n1)
  if [ -z "$group_entry" ]; then
    if command -v groupadd >/dev/null 2>&1; then
      groupadd --system oboard
    else
      addgroup -S oboard
    fi
    group_entry=$(getent group oboard 2>/dev/null || sed -n '/^oboard:/p' /etc/group | head -n1)
  fi
  [ -n "$group_entry" ] || { echo "无法创建 oboard 系统组。" >&2; exit 1; }
  printf '%s\n' "$group_entry" | cut -d: -f3
}

install_controller_updater() {
  arch=$(detect_arch)
  release_tag="v$TAG"
  artifact_version=$TAG
  case "$TAG" in
    dev) release_tag=dev; artifact_version=dev ;;
    latest|stable)
      artifact_version=$(latest_version)
      [ -n "$artifact_version" ] || { echo "无法获取最新正式版。" >&2; exit 1; }
      release_tag="v$artifact_version"
      ;;
  esac
  archive="oboard_controller_${artifact_version}_linux_${arch}.tar.gz"
  updater_tmp=$(mktemp -d)
  trap 'rm -rf "$updater_tmp"' EXIT INT TERM
  curl -fL "https://github.com/OboardProject/oboard/releases/download/$release_tag/$archive" -o "$updater_tmp/$archive"
  curl -fsSL "https://github.com/OboardProject/oboard/releases/download/$release_tag/sha256sums.txt" -o "$updater_tmp/sha256sums.txt"
  awk -v name="$archive" '$2 == name { print $1 "  " name; found=1 } END { exit found ? 0 : 1 }' "$updater_tmp/sha256sums.txt" > "$updater_tmp/checksum"
  (cd "$updater_tmp" && sha256sum -c checksum)
  if tar -tzf "$updater_tmp/$archive" | grep -E '(^/|(^|/)\.\.(/|$))' >/dev/null; then
    echo "主控安装包包含不安全路径。" >&2
    exit 1
  fi
  tar -xzf "$updater_tmp/$archive" -C "$updater_tmp"
  install -m 0755 "$updater_tmp/bin/oboard-controller-updater" /usr/local/bin/oboard-controller-updater.new
  mv -f /usr/local/bin/oboard-controller-updater.new /usr/local/bin/oboard-controller-updater
  if command -v systemctl >/dev/null 2>&1 && [ -d /run/systemd/system ]; then
    cp "$updater_tmp/deploy/systemd/oboard-controller-updater.service" /etc/systemd/system/
    systemctl daemon-reload
    systemctl enable oboard-controller-updater
    systemctl restart oboard-controller-updater
  elif command -v rc-service >/dev/null 2>&1; then
    cp "$updater_tmp/deploy/openrc/oboard-controller-updater" /etc/init.d/oboard-controller-updater
    chmod 0755 /etc/init.d/oboard-controller-updater
    rc-update add oboard-controller-updater default
    rc-service oboard-controller-updater restart
  else
    echo "主控面板更新需要 systemd 或 OpenRC。" >&2
    exit 1
  fi
  rm -rf "$updater_tmp"
  trap - EXIT INT TERM
}

generate_secret() {
  if command -v openssl >/dev/null 2>&1; then
    openssl rand -hex 32
  else
    od -An -N32 -tx1 /dev/urandom | tr -d ' \n'
  fi
}

generate_admin_password() {
  generate_secret | cut -c1-24
}

set_env_value() {
  key=$1 value=$2 tmp_env=$(mktemp)
  sed "/^${key}=/d" "$ENV_FILE" > "$tmp_env"
  printf '%s=%s\n' "$key" "$value" >> "$tmp_env"
  cat "$tmp_env" > "$ENV_FILE"
  rm -f "$tmp_env"
}

configure_bootstrap_admin() {
  local password_confirm
  [ "$FRESH_INSTALL" = 1 ] || return 0
  if [ -r /dev/tty ] && [ -z "$ADMIN_USERNAME_INPUT" ]; then
    printf '\n设置超级管理员\n' > /dev/tty
    printf '该账号会自动加入“管理员组”，并作为首位超级管理员受到保护，不能在面板中删除。\n' > /dev/tty
    printf '超级管理员用户名 [%s]：' "$ADMIN_USERNAME" > /dev/tty
    IFS= read -r password_confirm < /dev/tty || true
    [ -z "$password_confirm" ] || ADMIN_USERNAME=$password_confirm
  fi
  if [ -z "$ADMIN_USERNAME" ]; then
    echo "超级管理员用户名不能为空。" >&2
    exit 1
  fi

  if [ -r /dev/tty ] && [ -z "$ADMIN_PASSWORD_INPUT" ]; then
    while :; do
      printf '超级管理员密码（至少 10 位；留空则自动生成随机密码）：' > /dev/tty
      stty -echo < /dev/tty
      IFS= read -r ADMIN_PASSWORD < /dev/tty || true
      stty echo < /dev/tty
      printf '\n' > /dev/tty
      [ -z "$ADMIN_PASSWORD" ] && break
      printf '再次输入密码：' > /dev/tty
      stty -echo < /dev/tty
      IFS= read -r password_confirm < /dev/tty || true
      stty echo < /dev/tty
      printf '\n' > /dev/tty
      if [ "$ADMIN_PASSWORD" = "$password_confirm" ]; then
        break
      fi
      printf '两次输入的密码不一致，请重新输入。\n' > /dev/tty
    done
  fi
  if [ -n "$ADMIN_PASSWORD" ] && [ "${#ADMIN_PASSWORD}" -lt 10 ]; then
    echo "超级管理员密码至少需要 10 位。" >&2
    exit 1
  fi
  [ -n "$ADMIN_PASSWORD" ] || ADMIN_PASSWORD=$(generate_admin_password)
}

resolve_tag() {
  case "$VERSION_VALUE" in
    latest|stable|"") echo latest ;;
    dev|development|nightly) echo dev ;;
    v*) echo "${VERSION_VALUE#v}" ;;
    *) echo "$VERSION_VALUE" ;;
  esac
}

write_compose() {
  mkdir -p "$INSTALL_ROOT/data"
  cat > "$INSTALL_ROOT/docker-compose.yml" <<'YAML'
services:
  controller:
    image: "${OBOARD_IMAGE}:${OBOARD_TAG}"
    container_name: oboard-controller
    restart: unless-stopped
    ports:
      - "${OBOARD_PORT}:2787"
    environment:
      OBOARD_SESSION_SECRET: "${OBOARD_SESSION_SECRET}"
      OBOARD_BASE_PATH: "${OBOARD_BASE_PATH}"
      OBOARD_ADMIN_USERNAME: "${OBOARD_ADMIN_USERNAME}"
      OBOARD_ADMIN_PASSWORD: "${OBOARD_ADMIN_PASSWORD}"
      OBOARD_AUTO_ADMIN: "true"
      OBOARD_INSTALL_METHOD: "docker"
      OBOARD_UPDATE_CHANNEL: "${OBOARD_TAG}"
      OBOARD_BACKUP_DIR: "/app/data/backups"
      OBOARD_CONTROLLER_UPDATER_SOCKET: "/run/oboard/controller-updater.sock"
      TZ: "${TZ}"
    group_add:
      - "${OBOARD_UPDATER_GID}"
    volumes:
      - ./data:/app/data
      - /run/oboard:/run/oboard:ro
    read_only: true
    tmpfs:
      - /tmp:size=64m,mode=1777
    security_opt:
      - no-new-privileges:true
    cap_drop:
      - ALL
    cap_add:
      - CHOWN
      - SETGID
      - SETUID
YAML
}

write_env() {
  local tag=$1 env_file="$ENV_FILE"
  if [ ! -f "$env_file" ]; then
    umask 077
    cat > "$env_file" <<EOF
OBOARD_IMAGE=$IMAGE
OBOARD_TAG=$tag
OBOARD_PORT=$PORT
OBOARD_BASE_PATH=$BASE_PATH
OBOARD_SESSION_SECRET=$(generate_secret)
OBOARD_ADMIN_USERNAME=$ADMIN_USERNAME
OBOARD_ADMIN_PASSWORD=$ADMIN_PASSWORD
OBOARD_UPDATER_GID=$UPDATER_GID
TZ=$TIMEZONE
EOF
    return
  fi
  # Portable in-place edit for GNU sed and BusyBox sed (Alpine).
  tmp_env=$(mktemp)
  sed -e "s#^OBOARD_IMAGE=.*#OBOARD_IMAGE=$IMAGE#"       -e "s#^OBOARD_TAG=.*#OBOARD_TAG=$tag#"       -e "s#^OBOARD_PORT=.*#OBOARD_PORT=$PORT#"       -e "s#^OBOARD_BASE_PATH=.*#OBOARD_BASE_PATH=$BASE_PATH#"       "$env_file" > "$tmp_env"
  cat "$tmp_env" > "$env_file"
  rm -f "$tmp_env"
  grep -q '^OBOARD_BASE_PATH=' "$env_file" || printf 'OBOARD_BASE_PATH=%s\n' "$BASE_PATH" >> "$env_file"
  set_env_value OBOARD_UPDATER_GID "$UPDATER_GID"
  if [ "$FRESH_INSTALL" = 1 ]; then
    set_env_value OBOARD_ADMIN_USERNAME "$ADMIN_USERNAME"
    set_env_value OBOARD_ADMIN_PASSWORD "$ADMIN_PASSWORD"
  fi
}


wait_for_health() {
  local i status
  for i in $(seq 1 30); do
    status=$(docker inspect --format '{{if .State.Health}}{{.State.Health.Status}}{{else}}{{.State.Status}}{{end}}' oboard-controller 2>/dev/null || true)
    case "$status" in
      healthy) return 0 ;;
      unhealthy|exited|dead)
        echo "容器启动失败，最近日志：" >&2
        compose logs --tail=80 controller >&2 || true
        return 1
        ;;
    esac
    sleep 2
  done
  echo "等待主控健康检查超时，最近日志：" >&2
  compose logs --tail=80 controller >&2 || true
  return 1
}

print_result() {
  local tag=$1 admin_username admin_password
  admin_username=$(env_value OBOARD_ADMIN_USERNAME)
  admin_password=$(env_value OBOARD_ADMIN_PASSWORD)
  echo ""
  echo "========================================"
  echo "OBoard Docker 部署完成"
  echo "========================================"
  echo "面板地址：http://服务器IP:$PORT$BASE_PATH"
  echo "镜像：$IMAGE:$tag"
  echo "部署目录：$INSTALL_ROOT"
  echo "数据目录：$INSTALL_ROOT/data"
  echo ""
  if [ "$FRESH_INSTALL" = 1 ]; then
    echo "首次登录账号：${admin_username:-admin}"
    echo "首次登录密码：$admin_password"
    echo "该账号已自动加入“管理员组”，并作为首位超级管理员不能在面板中删除。"
    echo "登录后请立即修改密码。"
  else
    echo "原有账号和数据已保留。"
  fi
  echo ""
  echo "常用命令："
  echo "  cd $INSTALL_ROOT && docker compose ps"
  echo "  cd $INSTALL_ROOT && docker compose logs -f --tail=100"
  echo "  $INSTALL_ROOT/update.sh                 更新当前渠道"
  echo "  VERSION=dev $INSTALL_ROOT/update.sh     切换到开发版"
  echo "  VERSION=latest $INSTALL_ROOT/update.sh  切换到正式版"
  echo ""
  echo "Controller 和 Agent 相互独立，也可以安装在同一台服务器上。"
  echo "如需让本机同时作为节点，请登录面板添加本机服务器，再执行面板生成的 Agent 安装命令。"
  echo "========================================"
}

need_root
ensure_base_tools
install_docker_engine
start_docker_engine

case "$ACTION" in
  uninstall)
    if [ -f "$INSTALL_ROOT/docker-compose.yml" ] && [ -f "$INSTALL_ROOT/.env" ]; then
      compose down --remove-orphans
    fi
    if [ "${OBOARD_PURGE:-0}" = 1 ]; then
      rm -rf "$INSTALL_ROOT"
      echo "OBoard 容器和数据已删除。"
    else
      echo "OBoard 容器已删除，数据保留在 $INSTALL_ROOT/data。"
    fi
    exit 0
    ;;
  install|update) ;;
  *) echo "未知操作：$ACTION" >&2; exit 2 ;;
esac

TAG=$(resolve_tag)
UPDATER_GID=$(ensure_oboard_group)
configure_bootstrap_admin
write_compose
write_env "$TAG"
install_controller_updater
cat > "$INSTALL_ROOT/update.sh" <<'SH'
#!/bin/sh
set -eu
url="https://raw.githubusercontent.com/OboardProject/oboard/main/scripts/install-docker.sh"
root="${OBOARD_DOCKER_DIR:-/opt/oboard-docker}"
version=${VERSION:-$(sed -n 's/^OBOARD_TAG=//p' "$root/.env" 2>/dev/null | tail -n1)}
curl -fsSL "$url" | env OBOARD_ACTION=update OBOARD_DOCKER_DIR="$root" VERSION="${version:-latest}" sh
SH
chmod 0755 "$INSTALL_ROOT/update.sh"

echo "正在拉取镜像 $IMAGE:$TAG ..."
compose pull controller
echo "正在启动 OBoard Controller ..."
compose up -d --remove-orphans controller
wait_for_health
print_result "$TAG"

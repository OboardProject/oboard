#!/usr/bin/env bash
set -euo pipefail

CONTROLLER_DIR=$(CDPATH= cd -- "$(dirname -- "$0")/.." && pwd)
WORKSPACE_DIR=$(CDPATH= cd -- "$CONTROLLER_DIR/.." && pwd)
VERSION_VALUE=$(tr -d '[:space:]' < "$CONTROLLER_DIR/VERSION")
SSH_PORT=${SSH_PORT:-22}
HTTP_PORT=${HTTP_PORT:-2787}
PUBLIC_PORT=${PUBLIC_PORT:-}
BASE_PATH=${OBOARD_BASE_PATH:-}
REMOTE_TMP=${REMOTE_TMP:-/tmp/oboard-controller.tar.gz}

usage() {
  cat >&2 <<USAGE
Usage:
  $0 root@<server-ip> /path/to/private_key [ssh_port]

Environment:
  HTTP_PORT=2787        Controller internal listen port on remote host.
  PUBLIC_PORT=80        Optional nginx reverse-proxy public port.
  OBOARD_BASE_PATH=/abc Optional path prefix for every Controller endpoint.
  SSH_PORT=22           SSH port; overridden by third argument.
  OBOARD_FORCE_BUILD=1  Rebuild matching release artifact before upload.
  OBOARD_AGENT_RELEASE_DIR=...    Directory containing a signed Agent release.
  OBOARD_RELEASE_PUBLIC_KEY=...   Matching Ed25519 public key for that release.

Example:
  HTTP_PORT=2787 PUBLIC_PORT=80 $0 root@203.0.113.10 ~/.ssh/oboard_test 22
USAGE
}

if [ "${1:-}" = "-h" ] || [ "${1:-}" = "--help" ]; then
  usage
  exit 0
fi
if [ $# -lt 2 ]; then
  usage
  exit 2
fi

SSH_TARGET=$1
KEY_FILE=$2
if [ $# -ge 3 ]; then
  SSH_PORT=$3
fi
if [ ! -f "$KEY_FILE" ]; then
  echo "Private key not found: $KEY_FILE" >&2
  exit 2
fi
case "$SSH_PORT" in (*[!0-9]*|"") echo "SSH_PORT must be numeric: $SSH_PORT" >&2; exit 2 ;; esac
case "$HTTP_PORT" in (*[!0-9]*|"") echo "HTTP_PORT must be numeric: $HTTP_PORT" >&2; exit 2 ;; esac
if [ -n "$PUBLIC_PORT" ]; then
  case "$PUBLIC_PORT" in (*[!0-9]*|"") echo "PUBLIC_PORT must be numeric: $PUBLIC_PORT" >&2; exit 2 ;; esac
fi
case "$BASE_PATH" in
  ""|/) BASE_PATH= ;;
  /*) BASE_PATH=${BASE_PATH%/} ;;
  *) echo "OBOARD_BASE_PATH must start with /" >&2; exit 2 ;;
esac
case "$BASE_PATH" in
  *[!A-Za-z0-9/._~-]*|*//*|*/./*|*/../*|*/.|*/..)
    echo "OBOARD_BASE_PATH contains unsafe or ambiguous path characters" >&2
    exit 2
    ;;
esac

SSH_OPTS=(-i "$KEY_FILE" -p "$SSH_PORT" -o IdentitiesOnly=yes -o StrictHostKeyChecking=accept-new -o ConnectTimeout=10 -o ServerAliveInterval=15 -o ServerAliveCountMax=4)
# Prefer SSH for control commands. Uploads use the chunked helper below because
# some minimal test images expose flaky SFTP/SCP sessions for larger artifacts.
SCP_OPTS=(-O -i "$KEY_FILE" -P "$SSH_PORT" -o IdentitiesOnly=yes -o StrictHostKeyChecking=accept-new -o ConnectTimeout=10 -o ServerAliveInterval=15 -o ServerAliveCountMax=4)

upload_artifact() {
  local src=$1
  local dst=$2
  if ! command -v python3 >/dev/null 2>&1; then
    scp "${SCP_OPTS[@]}" "$src" "$SSH_TARGET:$dst"
    return
  fi
  python3 - "$KEY_FILE" "$SSH_PORT" "$SSH_TARGET" "$src" "$dst" <<'PY'
import hashlib
import os
import shlex
import subprocess
import sys
import time

key, port, target, src, dst = sys.argv[1:6]
chunk_size = int(os.environ.get("OBOARD_UPLOAD_CHUNK_SIZE", str(1024 * 1024)))
ssh_base = [
    "ssh",
    "-i", key,
    "-p", port,
    "-o", "IdentitiesOnly=yes",
    "-o", "StrictHostKeyChecking=accept-new",
    "-o", "ConnectTimeout=10",
    "-o", "ServerAliveInterval=15",
    "-o", "ServerAliveCountMax=4",
    target,
]
chunk_dir = f"{dst}.chunks"
part = f"{dst}.part"

def remote(cmd, data=None, timeout=60):
    return subprocess.run(ssh_base + [cmd], input=data, check=True, timeout=timeout)

remote(f"rm -rf {shlex.quote(chunk_dir)} {shlex.quote(part)} {shlex.quote(dst)} && mkdir -p {shlex.quote(chunk_dir)}", timeout=30)

size = os.path.getsize(src)
sent = 0
idx = 0
with open(src, "rb") as f:
    while True:
        chunk = f.read(chunk_size)
        if not chunk:
            break
        idx += 1
        chunk_path = f"{chunk_dir}/{idx:06d}"
        for attempt in range(1, 4):
            try:
                remote(f"cat > {shlex.quote(chunk_path)}", data=chunk, timeout=60)
                sent += len(chunk)
                print(f"    chunk {idx}: {sent}/{size}", flush=True)
                break
            except Exception as exc:
                print(f"    chunk {idx} attempt {attempt} failed: {exc}", flush=True)
                if attempt == 3:
                    raise
                time.sleep(2)

remote(f"cat {shlex.quote(chunk_dir)}/* > {shlex.quote(part)}", timeout=60)
local_sha = hashlib.sha256(open(src, "rb").read()).hexdigest()
remote_sha = subprocess.check_output(ssh_base + [f"sha256sum {shlex.quote(part)} | awk '{{print $1}}'"], timeout=30).decode().strip()
if local_sha != remote_sha:
    raise SystemExit(f"upload sha256 mismatch: local={local_sha} remote={remote_sha}")
remote(f"mv {shlex.quote(part)} {shlex.quote(dst)} && rm -rf {shlex.quote(chunk_dir)}", timeout=30)
print(f"    uploaded {size} bytes, sha256={local_sha}", flush=True)
PY
}

remote_arch=$(ssh "${SSH_OPTS[@]}" "$SSH_TARGET" 'uname -m')
case "$remote_arch" in
  x86_64|amd64) arch=amd64 ;;
  aarch64|arm64) arch=arm64 ;;
  *) echo "Unsupported remote architecture: $remote_arch" >&2; exit 1 ;;
esac

artifact="$CONTROLLER_DIR/dist/release/oboard_controller_${VERSION_VALUE}_linux_${arch}.tar.gz"
if [ "${OBOARD_FORCE_BUILD:-0}" = "1" ] || [ ! -f "$artifact" ]; then
  echo "==> Building controller artifact for linux/$arch"
  OBOARD_PLATFORMS="linux/$arch" "$CONTROLLER_DIR/scripts/build-release.sh"
fi
if [ ! -f "$artifact" ]; then
  echo "Artifact not found after build: $artifact" >&2
  exit 1
fi

if command -v openssl >/dev/null 2>&1; then
  session_secret=$(openssl rand -base64 48 | tr -d '\n')
else
  session_secret=$(python3 - <<'PY'
import base64, os
print(base64.b64encode(os.urandom(48)).decode(), end='')
PY
)
fi

echo "==> Uploading $artifact to $SSH_TARGET:$REMOTE_TMP"
upload_artifact "$artifact" "$REMOTE_TMP"

echo "==> Installing and starting OBoard controller on $SSH_TARGET"
ssh "${SSH_OPTS[@]}" "$SSH_TARGET" \
  "OBOARD_HTTP_PORT='$HTTP_PORT' OBOARD_PUBLIC_PORT='$PUBLIC_PORT' OBOARD_BASE_PATH='$BASE_PATH' OBOARD_SESSION_SECRET_NEW='$session_secret' OBOARD_ARCHIVE='$REMOTE_TMP' bash -s" <<'REMOTE'
set -euo pipefail

if [ "$(id -u)" != "0" ]; then
  echo "Remote installer must run as root" >&2
  exit 1
fi
if ! command -v systemctl >/dev/null 2>&1 || [ ! -d /run/systemd/system ]; then
  echo "This test deploy script currently expects a systemd Linux server." >&2
  exit 1
fi

work=$(mktemp -d)
cleanup() { rm -rf "$work"; }
trap cleanup EXIT

tar -xzf "$OBOARD_ARCHIVE" -C "$work"

if ! id oboard >/dev/null 2>&1; then
  useradd --system --home /var/lib/oboard --shell /usr/sbin/nologin oboard
fi
install -d -m 0750 -o oboard -g oboard /var/lib/oboard /opt/oboard/web /opt/oboard/downloads /etc/oboard
install -m 0755 "$work/bin/oboard-controller" /usr/local/bin/oboard-controller
rm -rf /opt/oboard/web/dist.new
cp -R "$work/web/dist" /opt/oboard/web/dist.new
chown -R oboard:oboard /opt/oboard/web/dist.new
rm -rf /opt/oboard/web/dist
mv /opt/oboard/web/dist.new /opt/oboard/web/dist
if [ -d "$work/downloads" ]; then
  rm -rf /opt/oboard/downloads.new
  cp -R "$work/downloads" /opt/oboard/downloads.new
  chown -R oboard:oboard /opt/oboard/downloads.new
  rm -rf /opt/oboard/downloads
  mv /opt/oboard/downloads.new /opt/oboard/downloads
fi

if [ ! -f /etc/oboard/controller.env ]; then
  install -m 0600 -o root -g root /dev/null /etc/oboard/controller.env
  cat > /etc/oboard/controller.env <<EOF
OBOARD_SESSION_SECRET=$OBOARD_SESSION_SECRET_NEW
OBOARD_ADDR=:$OBOARD_HTTP_PORT
OBOARD_BASE_PATH=$OBOARD_BASE_PATH
OBOARD_DB=/var/lib/oboard/oboard.sqlite
OBOARD_STATIC=/opt/oboard/web/dist
OBOARD_DOWNLOADS=/opt/oboard/downloads
OBOARD_CORS_ORIGINS=
EOF
else
  if grep -q '^OBOARD_ADDR=' /etc/oboard/controller.env; then
    sed -i "s#^OBOARD_ADDR=.*#OBOARD_ADDR=:$OBOARD_HTTP_PORT#" /etc/oboard/controller.env
  else
    printf '\nOBOARD_ADDR=:%s\n' "$OBOARD_HTTP_PORT" >> /etc/oboard/controller.env
  fi
  if ! grep -q '^OBOARD_SESSION_SECRET=' /etc/oboard/controller.env; then
    printf 'OBOARD_SESSION_SECRET=%s\n' "$OBOARD_SESSION_SECRET_NEW" >> /etc/oboard/controller.env
  fi
  if grep -q '^OBOARD_BASE_PATH=' /etc/oboard/controller.env; then
    sed -i "s#^OBOARD_BASE_PATH=.*#OBOARD_BASE_PATH=$OBOARD_BASE_PATH#" /etc/oboard/controller.env
  else
    printf 'OBOARD_BASE_PATH=%s\n' "$OBOARD_BASE_PATH" >> /etc/oboard/controller.env
  fi
  if ! grep -q '^OBOARD_DB=' /etc/oboard/controller.env; then
    printf 'OBOARD_DB=/var/lib/oboard/oboard.sqlite\n' >> /etc/oboard/controller.env
  fi
  if ! grep -q '^OBOARD_STATIC=' /etc/oboard/controller.env; then
    printf 'OBOARD_STATIC=/opt/oboard/web/dist\n' >> /etc/oboard/controller.env
  fi
  if grep -q '^OBOARD_DOWNLOADS=' /etc/oboard/controller.env; then
    sed -i "s#^OBOARD_DOWNLOADS=.*#OBOARD_DOWNLOADS=/opt/oboard/downloads#" /etc/oboard/controller.env
  else
    printf 'OBOARD_DOWNLOADS=/opt/oboard/downloads\n' >> /etc/oboard/controller.env
  fi
  chmod 0600 /etc/oboard/controller.env
fi

cp "$work/deploy/systemd/oboard-controller.service" /etc/systemd/system/oboard-controller.service
systemctl daemon-reload
systemctl enable oboard-controller >/dev/null
systemctl restart oboard-controller
sleep 1
systemctl --no-pager --full status oboard-controller | sed -n '1,18p'
curl -fsS "http://127.0.0.1:$OBOARD_HTTP_PORT$OBOARD_BASE_PATH/healthz" >/dev/null

if [ -n "${OBOARD_PUBLIC_PORT:-}" ] && [ "$OBOARD_PUBLIC_PORT" != "$OBOARD_HTTP_PORT" ]; then
  if ! command -v nginx >/dev/null 2>&1; then
    if command -v apt-get >/dev/null 2>&1; then
      apt-get update
      DEBIAN_FRONTEND=noninteractive apt-get install -y nginx
    elif command -v dnf >/dev/null 2>&1; then
      dnf install -y nginx
    elif command -v yum >/dev/null 2>&1; then
      yum install -y nginx
    elif command -v apk >/dev/null 2>&1; then
      apk add --no-cache nginx
    else
      echo "nginx is required for PUBLIC_PORT=$OBOARD_PUBLIC_PORT but no supported package manager was found" >&2
      exit 1
    fi
  fi
  install -d -m 0755 /etc/nginx/conf.d
  rm -f /etc/nginx/sites-enabled/default 2>/dev/null || true
  cat > /etc/nginx/conf.d/oboard.conf <<EOF
server {
    listen $OBOARD_PUBLIC_PORT default_server;
    server_name _;

    client_max_body_size 2m;

    location / {
        proxy_http_version 1.1;
        proxy_set_header Host \$host;
        proxy_set_header X-Real-IP \$remote_addr;
        proxy_set_header X-Forwarded-For \$proxy_add_x_forwarded_for;
        proxy_set_header X-Forwarded-Proto \$scheme;
        proxy_set_header Upgrade \$http_upgrade;
        proxy_set_header Connection "upgrade";
        proxy_pass http://127.0.0.1:$OBOARD_HTTP_PORT;
    }
}
EOF
  nginx -t
  systemctl enable nginx >/dev/null
  systemctl restart nginx
  echo "nginx reverse proxy is listening on :$OBOARD_PUBLIC_PORT"
fi

echo "OBoard controller health check passed on 127.0.0.1:$OBOARD_HTTP_PORT$OBOARD_BASE_PATH"
REMOTE

echo "==> Done"
if [ -n "$PUBLIC_PORT" ]; then
  echo "Open: http://${SSH_TARGET#*@}:$PUBLIC_PORT$BASE_PATH"
else
  echo "Open: http://${SSH_TARGET#*@}:$HTTP_PORT$BASE_PATH"
fi
echo "Then create the first admin account from the bootstrap page."

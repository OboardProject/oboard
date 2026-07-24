#!/bin/sh
set -eu

if [ "$(id -u)" -eq 0 ]; then
  mkdir -p /app/data
  # Best-effort ownership fix only. The container drops most capabilities, so
  # recursive chown can fail on 0700 data files without CAP_DAC_OVERRIDE.
  chown oboard:oboard /app/data 2>/dev/null || true
  chown -R oboard:oboard /app/data 2>/dev/null || true
  uid=$(id -u oboard)
  gid=$(id -g oboard)
  # Docker group_add alone is not enough: su-exec replaces supplementary groups.
  # BusyBox setpriv also cannot keep them. Use the host updater GID as the
  # process primary group so 0660 root:oboard sockets under /run/oboard work.
  if [ -n "${OBOARD_UPDATER_GID:-}" ]; then
    exec su-exec "$uid:$OBOARD_UPDATER_GID" "$@"
  fi
  sock=${OBOARD_CONTROLLER_UPDATER_SOCKET:-/run/oboard/controller-updater.sock}
  if [ -S "$sock" ] && command -v stat >/dev/null 2>&1; then
    sock_gid=$(stat -c %g "$sock" 2>/dev/null || stat -f %g "$sock" 2>/dev/null || true)
    if [ -n "$sock_gid" ]; then
      exec su-exec "$uid:$sock_gid" "$@"
    fi
  fi
  exec su-exec "$uid:$gid" "$@"
fi

exec "$@"

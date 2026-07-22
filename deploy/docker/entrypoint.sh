#!/bin/sh
set -eu

if [ "$(id -u)" -eq 0 ]; then
  mkdir -p /app/data
  chown -R oboard:oboard /app/data
  exec su-exec oboard:oboard "$@"
fi

exec "$@"

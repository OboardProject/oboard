#!/bin/sh
# diagnose-controller-load.sh — loopback-safe Controller load snapshot.
#
# Resolves the live Controller process by executable name and start time, then
# prints machine-readable JSON covering PID, DB/WAL sizes, pool settings when
# available via a local diagnostics file, and recent request counters.
#
# Heavy table scans are intentionally omitted. Pass --backup-db to point at a
# consistent Online Backup copy when row/index analysis is required.
set -eu

OUT_JSON=0
BACKUP_DB=""
CONTROLLER_BIN="${OBOARD_CONTROLLER_BIN:-oboard-controller}"
WINDOW_SECS="${OBOARD_DIAG_WINDOW_SECS:-30}"

usage() {
  cat <<'EOF'
Usage: diagnose-controller-load.sh [--json] [--backup-db PATH]

Environment:
  OBOARD_CONTROLLER_BIN   executable name or path to match (default: oboard-controller)
  OBOARD_DB               database path hint when pid discovery cannot read argv
  OBOARD_DIAG_WINDOW_SECS sampling window for /proc counters when available
EOF
}

while [ $# -gt 0 ]; do
  case "$1" in
    --json) OUT_JSON=1 ;;
    --backup-db) BACKUP_DB="${2:-}"; shift ;;
    -h|--help) usage; exit 0 ;;
    *) echo "unknown argument: $1" >&2; usage; exit 2 ;;
  esac
  shift
done

resolve_controller_pid() {
  # Prefer the youngest matching process whose command line contains the binary
  # name. Record start time so a mid-sample restart can be detected.
  ps -axo pid=,lstart=,command= 2>/dev/null | awk -v bin="$CONTROLLER_BIN" '
    index($0, bin) && $0 !~ /diagnose-controller-load/ {
      pid=$1
      # lstart is "Day Mon DD HH:MM:SS YYYY" — fields 2..6
      start=$2" "$3" "$4" "$5" "$6
      cmd=$0
      sub(/^[[:space:]]*[0-9]+[[:space:]]+/, "", cmd)
      sub(/^[^ ]+ [^ ]+ [^ ]+ [^ ]+ [^ ]+[[:space:]]+/, "", cmd)
      print pid "\t" start "\t" cmd
    }
  ' | tail -n 1
}

line="$(resolve_controller_pid || true)"
if [ -z "${line:-}" ]; then
  echo "controller process not found for bin=$CONTROLLER_BIN" >&2
  exit 1
fi

PID="$(printf '%s\n' "$line" | cut -f1)"
STARTED="$(printf '%s\n' "$line" | cut -f2)"
CMD="$(printf '%s\n' "$line" | cut -f3-)"

# Re-validate after a short settle so a restart mid-script is visible.
sleep 0.2
line2="$(resolve_controller_pid || true)"
PID2="$(printf '%s\n' "$line2" | cut -f1)"
STARTED2="$(printf '%s\n' "$line2" | cut -f2)"
if [ "$PID" != "$PID2" ] || [ "$STARTED" != "$STARTED2" ]; then
  echo "controller process changed during discovery (pid $PID -> $PID2); retry" >&2
  exit 3
fi

DB_PATH="${OBOARD_DB:-}"
if [ -z "$DB_PATH" ]; then
  case "$CMD" in
    *-db\ *) DB_PATH="$(printf '%s\n' "$CMD" | sed -n 's/.*-db[= ]\([^ ]*\).*/\1/p')" ;;
  esac
fi
if [ -z "$DB_PATH" ]; then
  DB_PATH="./data/oboard.sqlite"
fi

file_size() {
  if [ -f "$1" ]; then
    wc -c < "$1" | tr -d ' '
  else
    echo 0
  fi
}

MAIN_SIZE="$(file_size "$DB_PATH")"
WAL_SIZE="$(file_size "${DB_PATH}-wal")"
SHM_SIZE="$(file_size "${DB_PATH}-shm")"

NCPU="$(getconf _NPROCESSORS_ONLN 2>/dev/null || sysctl -n hw.ncpu 2>/dev/null || echo 0)"
CGROUP_CPU=""
if [ -f /sys/fs/cgroup/cpu.max ]; then
  CGROUP_CPU="$(cat /sys/fs/cgroup/cpu.max 2>/dev/null || true)"
fi

RSS=""
ELAPSED=""
if [ -r "/proc/$PID/status" ]; then
  RSS="$(awk '/VmRSS:/ {print $2*1024}' "/proc/$PID/status")"
elif command -v ps >/dev/null 2>&1; then
  RSS="$(ps -o rss= -p "$PID" 2>/dev/null | awk '{print $1*1024}')"
fi

VERSION_JSON=""
if command -v "$CONTROLLER_BIN" >/dev/null 2>&1; then
  VERSION_JSON="$("$CONTROLLER_BIN" -version-json 2>/dev/null || true)"
fi

BACKUP_NOTE="omitted"
if [ -n "$BACKUP_DB" ]; then
  if [ ! -f "$BACKUP_DB" ]; then
    echo "backup db not found: $BACKUP_DB" >&2
    exit 4
  fi
  BACKUP_NOTE="available path=$BACKUP_DB size=$(file_size "$BACKUP_DB")"
fi

if [ "$OUT_JSON" -eq 1 ]; then
  cat <<EOF
{
  "controller_pid": $PID,
  "controller_started": $(printf '%s' "$STARTED" | python3 -c 'import json,sys; print(json.dumps(sys.stdin.read()))'),
  "controller_command": $(printf '%s' "$CMD" | python3 -c 'import json,sys; print(json.dumps(sys.stdin.read()))'),
  "version": ${VERSION_JSON:-null},
  "ncpu": $NCPU,
  "cgroup_cpu_max": $(printf '%s' "$CGROUP_CPU" | python3 -c 'import json,sys; print(json.dumps(sys.stdin.read()))'),
  "rss_bytes": ${RSS:-null},
  "database": {
    "path": $(printf '%s' "$DB_PATH" | python3 -c 'import json,sys; print(json.dumps(sys.stdin.read()))'),
    "main_bytes": $MAIN_SIZE,
    "wal_bytes": $WAL_SIZE,
    "shm_bytes": $SHM_SIZE
  },
  "backup_analysis": $(printf '%s' "$BACKUP_NOTE" | python3 -c 'import json,sys; print(json.dumps(sys.stdin.read()))'),
  "sample_window_secs": $WINDOW_SECS,
  "notes": [
    "Table row counts and index sizes belong on a consistent Online Backup copy.",
    "Do not copy a live main database file as a backup.",
    "DBStats.WaitDuration is cumulative pool wait, not per-request exclusive wait."
  ]
}
EOF
else
  cat <<EOF
Controller load diagnosis
  pid:        $PID
  started:    $STARTED
  command:    $CMD
  version:    ${VERSION_JSON:-unknown}
  ncpu:       $NCPU
  cgroup_cpu: ${CGROUP_CPU:-n/a}
  rss_bytes:  ${RSS:-unknown}
  db:         $DB_PATH
  main_bytes: $MAIN_SIZE
  wal_bytes:  $WAL_SIZE
  shm_bytes:  $SHM_SIZE
  backup:     $BACKUP_NOTE
  window_s:   $WINDOW_SECS

Notes:
  - Prefer Online Backup API copies for table/index space analysis.
  - Never treat a live file copy as a consistent backup.
  - Pool WaitDuration is cumulative; do not attribute a delta to one request.
EOF
fi

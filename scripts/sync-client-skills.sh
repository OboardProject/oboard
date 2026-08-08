#!/bin/sh
set -eu

repo_root=$(CDPATH= cd -- "$(dirname -- "$0")/.." && pwd)
source_file="$repo_root/skills/oboard/SKILL.md"
mode=${1:-write}

case "$mode" in
  write|check) ;;
  *)
    echo "usage: $0 [write|check]" >&2
    exit 2
    ;;
esac

for target in \
  "$repo_root/.agents/skills/oboard/SKILL.md" \
  "$repo_root/.claude/skills/oboard/SKILL.md" \
  "$repo_root/.opencode/skills/oboard/SKILL.md" \
  "$repo_root/.pi/skills/oboard/SKILL.md"
do
  if [ "$mode" = check ]; then
    cmp -s "$source_file" "$target" || {
      echo "generated OBoard skill is stale: $target" >&2
      exit 1
    }
    continue
  fi
  mkdir -p "$(dirname -- "$target")"
  cp "$source_file" "$target"
done

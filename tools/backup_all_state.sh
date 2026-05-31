#!/usr/bin/env bash
set -euo pipefail

SRC="/opt/coinbase/state"
DST="$SRC/backup"
STAMP="$(date -u +%Y%m%d-%H%M%SZ)"
RETENTION_DAYS="${RETENTION_DAYS:-14}"   # keep last 14 days; override via env if you want

mkdir -p "$DST"
exec 9>"/tmp/backup_all_state.lock"; flock -n 9 || exit 0

# backup every state json (coinbase, binance, hitbtc, etc.)
for f in "$SRC"/bot_state.*.json; do
  [ -s "$f" ] || continue
  # validate JSON (skip corrupt files)
  if jq empty "$f" 2>/dev/null; then
    base="$(basename "$f")"
    cp -a "$f" "$DST/${base}.${STAMP}" && gzip -f "$DST/${base}.${STAMP}"
    echo "Backed up: $base -> ${base}.${STAMP}.gz"
  else
    echo "WARN: invalid JSON, skipping $f" >&2
  fi
done

# rotate: delete older than N days
find "$DST" -type f -name 'bot_state.*.json.*' -mtime +"$RETENTION_DAYS" -print -delete || true

#!/usr/bin/env bash
# equity-wait-and-apply.sh
# ---
# Wait until EquityUSD increases by <amount> (±tolerance), then stop the service,
# update LastAddEquitySell and LastAddEquityBuy by +<amount>, and (optionally) restart.
#
# Usage:
#   equity-wait-and-apply.sh <amount> <coinbase|binance|hitbtc> [state_file] [--tolerance 1.0] [--timeout 0]
#
# Example:
#   equity-wait-and-apply.sh 50 coinbase
#
# Install (later):
#   sudo install -m 0755 ./tools/equity-wait-and-apply.sh /usr/local/bin/equity-wait-and-apply.sh

set -Eeuo pipefail

# Where docker-compose.yml lives
COMPOSE_DIR="/home/chidi/coinbase/monitoring"

# Map service -> docker compose service name
svc_compose() {
  case "$1" in
    coinbase) echo "bot" ;;
    binance)  echo "bot_binance" ;;
    hitbtc)   echo "bot_hitbtc" ;;
    *)        echo "unknown" ;;
  esac
}

# Map service -> default state file (your bot uses *.new*.json)
svc_statefile() {
  case "$1" in
    coinbase) echo "/opt/coinbase/state/bot_state.newcoinbase.json" ;;
    binance)  echo "/opt/coinbase/state/bot_state.newbinance.json" ;;
    hitbtc)   echo "/opt/coinbase/state/bot_state.hitbtc.json" ;;
    *)        echo "" ;;
  esac
}

usage() {
  cat <<EOF
Usage: $0 <amount> <coinbase|binance|hitbtc> [state_file] [--tolerance 1.0] [--timeout 0]

Waits until EquityUSD increases by approximately <amount> (±tolerance),
then stops the service, increases LastAddEquitySell and LastAddEquityBuy by <amount>,
and prompts to restart.

Examples:
  $0 50 coinbase
  $0 50 binance /opt/coinbase/state/bot_state.newbinance.json --tolerance 0.5 --timeout 600
EOF
  exit 1
}

# --- Parse args ---
[[ $# -lt 2 ]] && usage
AMOUNT="$1"; shift
SERVICE="$1"; shift

COMPOSE_SVC="$(svc_compose "$SERVICE")"
[[ "$COMPOSE_SVC" == "unknown" ]] && { echo "Unknown service: ${SERVICE}"; exit 2; }

STATE_FILE_DEFAULT="$(svc_statefile "$SERVICE")"
STATE_FILE="${STATE_FILE_DEFAULT}"

if [[ $# -gt 0 && "${1:-}" != --* ]]; then
  STATE_FILE="$1"; shift
fi
[[ -z "$STATE_FILE" ]] && { echo "No default state file known for ${SERVICE}; pass it explicitly."; exit 2; }

TOL=1.0
TIMEOUT=0
while [[ $# -gt 0 ]]; do
  case "$1" in
    --tolerance) shift; TOL="${1:-1.0}"; shift ;;
    --timeout)   shift; TIMEOUT="${1:-0}"; shift ;;
    *) echo "Unknown option: $1"; usage ;;
  esac
done

# --- Requirements ---
command -v jq >/dev/null || { echo "jq is required (sudo apt-get install -y jq)"; exit 3; }
command -v docker >/dev/null || { echo "docker is required"; exit 3; }
command -v awk >/dev/null || { echo "awk is required"; exit 3; }

# Prefer docker compose plugin if present, else docker-compose
if command -v docker compose >/dev/null 2>&1; then
  COMPOSE_CMD="docker compose"
elif command -v docker-compose >/dev/null 2>&1; then
  COMPOSE_CMD="docker-compose"
else
  echo "docker compose plugin (or docker-compose) required"
  exit 3
fi

# --- Helpers ---
read_json_key() { jq -r ".$2 // empty" "$1"; }

edit_json_inplace_preserve_meta() {
  # Write via temp, then restore owner/perms and atomically move over
  local file="$1" expr="$2" tmp owner group mode
  owner=$(stat -c '%u' "$file")
  group=$(stat -c '%g' "$file")
  mode=$(stat -c '%a' "$file")
  tmp="$(mktemp --suffix=.json "$(dirname "$file")/.$(basename "$file").XXXX")"
  jq "$expr" "$file" > "$tmp"
  # Restore metadata before replace
  chown "$owner:$group" "$tmp"
  chmod "$mode" "$tmp"
  mv -f "$tmp" "$file"
  sync
}

# --- Validate & baseline ---
[[ -r "$STATE_FILE" ]] || { echo "State file not readable: $STATE_FILE"; exit 4; }

BASE_EQ="$(read_json_key "$STATE_FILE" "EquityUSD")"
[[ -n "$BASE_EQ" ]] || { echo "EquityUSD not found in $STATE_FILE"; exit 5; }

TARGET_LOW=$(awk -v b="$BASE_EQ" -v a="$AMOUNT" -v t="$TOL" 'BEGIN{printf "%.6f", b + a - t}')
TARGET_HIGH=$(awk -v b="$BASE_EQ" -v a="$AMOUNT" -v t="$TOL" 'BEGIN{printf "%.6f", b + a + t}')

echo "Monitoring ${STATE_FILE} every 1s until EquityUSD rises by ~${AMOUNT} (±${TOL})."
echo "Baseline EquityUSD=${BASE_EQ}. Target band: [${TARGET_LOW}, ${TARGET_HIGH}]"
echo

# --- Poll loop ---
start_ts=$(date +%s)
while true; do
  CUR_EQ="$(read_json_key "$STATE_FILE" "EquityUSD" || true)"
  [[ -z "$CUR_EQ" ]] && CUR_EQ="NaN"
  printf "\r%(%F %T)T EquityUSD=%s  (looking for ~ +%s)" -1 "$CUR_EQ" "$AMOUNT" >&2

  if [[ "$CUR_EQ" != "NaN" ]]; then
    within=$(awk -v x="$CUR_EQ" -v lo="$TARGET_LOW" -v hi="$TARGET_HIGH" 'BEGIN{print (x>=lo && x<=hi) ? "yes" : "no"}')
    if [[ "$within" == "yes" ]]; then
      echo -e "\nTarget met: EquityUSD in band [${TARGET_LOW}, ${TARGET_HIGH}] (current=${CUR_EQ})"
      break
    fi
  fi

  if (( TIMEOUT > 0 )) && (( $(date +%s) - start_ts >= TIMEOUT )); then
    echo -e "\nTimeout (${TIMEOUT}s) reached without meeting target band. Exiting."
    exit 6
  fi
  sleep 1
done

# --- Stop service ---
echo "Stopping service: ${COMPOSE_SVC}"
pushd "$COMPOSE_DIR" >/dev/null
$COMPOSE_CMD stop "$COMPOSE_SVC"
popd >/dev/null

# --- Backup (preserve owner/perms) ---
ts="$(date +%Y%m%d%H%M%S)"
owner=$(stat -c '%u' "$STATE_FILE")
group=$(stat -c '%g' "$STATE_FILE")
mode=$(stat -c '%a' "$STATE_FILE")
backup="${STATE_FILE}.bak.${ts}"

cp -a "$STATE_FILE" "$backup" || { echo "Backup failed (need permissions?). Aborting."; exit 7; }
chown "$owner:$group" "$backup"
chmod "$mode" "$backup"
echo "Backup created: $backup"

# --- Apply both bumps ---
echo "Updating LastAddEquitySell and LastAddEquityBuy by +${AMOUNT} in ${STATE_FILE}"
edit_json_inplace_preserve_meta "$STATE_FILE" \
  ".LastAddEquitySell = ((.LastAddEquitySell // 0) + (${AMOUNT}|tonumber)) |
   .LastAddEquityBuy  = ((.LastAddEquityBuy  // 0) + (${AMOUNT}|tonumber))"

NEW_EQ="$(read_json_key "$STATE_FILE" "EquityUSD" || true)"
NEW_LAES="$(read_json_key "$STATE_FILE" "LastAddEquitySell" || true)"
NEW_LAEB="$(read_json_key "$STATE_FILE" "LastAddEquityBuy"  || true)"

echo "Updated values:"
echo "  EquityUSD:           ${NEW_EQ}"
echo "  LastAddEquitySell:   ${NEW_LAES}"
echo "  LastAddEquityBuy:    ${NEW_LAEB}"

# --- Prompt restart ---
read -r -p "Restart service '${COMPOSE_SVC}' now? [y/N]: " ans
if [[ "${ans}" =~ ^[Yy]$ ]]; then
  pushd "$COMPOSE_DIR" >/dev/null
  $COMPOSE_CMD start "$COMPOSE_SVC"
  popd >/dev/null
  echo "Service restarted."
else
  echo "Service NOT restarted. Start later with:"
  echo "  (cd ${COMPOSE_DIR} && $COMPOSE_CMD start ${COMPOSE_SVC})"
fi


# integration steps:
#  sudo nano /home/chidi/coinbase/tools/equity-wait-and-apply.sh
# sudo chmod +x /home/chidi/coinbase/tools/equity-wait-and-apply.sh
# sudo install -m 0755 /home/chidi/coinbase/tools/equity-wait-and-apply.sh /usr/local/bin/equity-wait-and-apply.sh
# sudo equity-wait-and-apply.sh 200 coinbase  
# v1
# /usr/local/bin/equity-wait-and-apply.sh
#!/usr/bin/env bash
# ------------------------------------------------------------------------------
#
# Purpose:
#   Take an investment fund amount and service (coinbase|binance|hitbtc), then:
#     1) Poll the bot state file every second until EquityUSD increases by ~<amount> (±tolerance).
#     2) Stop the docker compose service.
#     3) Increase LastAddEquitySell by <amount> in the state file (with a backup).
#     4) Show updated values and ask to restart the service.
#
# Quick usage (after you copy this into /usr/local/bin and make executable):
#   sudo install -m 0755 ./tools/equity-wait-and-apply.sh /usr/local/bin/equity-wait-and-apply.sh
#   equity-wait-and-apply.sh 200 coinbase
#
# Options:
#   equity-wait-and-apply.sh <amount> <coinbase|binance|hitbtc> [state_file] [--tolerance 1.0] [--timeout 0]
#
# Notes:
#   - Uses JSON keys with proper case: EquityUSD and LastAddEquitySell.
#   - Default tolerance is ±1.0; default timeout is 0 (no timeout).
#   - Auto-elevates to root so it can stop services and edit files under /opt/coinbase/state/.

set -Eeuo pipefail

# --- auto-elevate to root for file edits and docker compose control ---
if [[ $EUID -ne 0 ]]; then
  exec sudo /bin/bash "$0" "$@"
fi

# --- PATHS / SETTINGS ---
COMPOSE_DIR="/home/chidi/coinbase/monitoring"

# Map service name -> docker compose service
svc_compose() {
  case "$1" in
    coinbase) echo "bot" ;;
    binance)  echo "bot_binance" ;;
    hitbtc)   echo "bot_hitbtc" ;;
    *)        echo "unknown" ;;
  esac
}

# Map service name -> default state file path
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
then stops the service, increases LastAddEquitySell by <amount>, and prompts to restart.

Examples:
  $0 200 coinbase
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
if ! command -v docker >/dev/null; then
  echo "docker is required"; exit 3
fi

compose_cmd() {
  if command -v docker >/dev/null 2>&1 && docker compose version >/dev/null 2>&1; then
    echo "docker compose"
  elif command -v docker-compose >/dev/null 2>&1; then
    echo "docker-compose"
  else
    echo ""
  fi
}
COMPOSE_BIN="$(compose_cmd)"
[[ -z "$COMPOSE_BIN" ]] && { echo "docker compose plugin (or docker-compose) required"; exit 3; }

# --- Helpers ---
read_json_key() { jq -r ".$2 // empty" "$1"; }
edit_json_inplace() {
  local file="$1" expr="$2" tmp
  tmp="$(mktemp "${file}.XXXX")"
  jq "${expr}" "$file" >"$tmp"
  mv -f "$tmp" "$file"
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
    end_if=true
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
$COMPOSE_BIN stop "$COMPOSE_SVC"
popd >/dev/null

# --- Apply LastAddEquitySell += AMOUNT ---
echo "Updating LastAddEquitySell += ${AMOUNT} in ${STATE_FILE}"
cp -a "$STATE_FILE" "${STATE_FILE}.bak.$(date +%Y%m%d%H%M%S)"

# If key is missing/null, coalesce to 0 before adding.
edit_json_inplace "$STATE_FILE" ".LastAddEquitySell = ((.LastAddEquitySell // 0) + (${AMOUNT}|tonumber))"

NEW_EQ="$(read_json_key "$STATE_FILE" "EquityUSD" || true)"
NEW_LAES="$(read_json_key "$STATE_FILE" "LastAddEquitySell" || true)"

echo "Updated values:"
echo "  EquityUSD:          ${NEW_EQ}"
echo "  LastAddEquitySell:  ${NEW_LAES}"

# --- Prompt restart ---
read -r -p "Restart service '${COMPOSE_SVC}' now? [y/N]: " ans
if [[ "${ans}" =~ ^[Yy]$ ]]; then
  pushd "$COMPOSE_DIR" >/dev/null
  $COMPOSE_BIN start "$COMPOSE_SVC"
  popd >/dev/null
  echo "Service restarted."
else
  echo "Service NOT restarted. Start later with:"
  echo "  (cd ${COMPOSE_DIR} && $COMPOSE_BIN start ${COMPOSE_SVC})"
fi


# integration steps:
#  sudo nano /home/chidi/coinbase/tools/equity-wait-and-apply.sh
# sudo chmod +x /home/chidi/coinbase/tools/equity-wait-and-apply.sh
# sudo install -m 0755 /home/chidi/coinbase/tools/equity-wait-and-apply.sh /usr/local/bin/equity-wait-and-apply.sh
# sudo equity-wait-and-apply.sh 200 coinbase  



#!/bin/bash
# equity-wait-and-apply.sh
#
# Purpose:
#   Take an investment fund amount and service (coinbase|binance|hitbtc), then:
#     1) Poll the bot state file every second until EquityUSD increases by ~<amount> (±tolerance).
#     2) Stop the docker compose service.
#     3) Increase LastAddEquitySell AND LastAddEquityBuy by <amount> in the state file (with a backup).
#     4) Show updated values and ask to restart the service.
#
# Quick usage:
#   sudo install -m 0755 ./tools/equity-wait-and-apply.sh /usr/local/bin/equity-wait-and-apply.sh
#   equity-wait-and-apply.sh 200 coinbase
#
# Options:
#   equity-wait-and-apply.sh <amount> <coinbase|binance|hitbtc> [state_file] [--tolerance 1.0] [--timeout 0]
#
# Notes:
#   - Uses JSON keys with proper case: EquityUSD, LastAddEquitySell, LastAddEquityBuy.
#   - Default tolerance is ±1.0; default timeout is 0 (no timeout).
#   - Auto-elevates to root so it can stop services and edit files under /opt/coinbase/state/.

set -Eeuo pipefail

# --- auto-elevate to root ---
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
then stops the service, increases LastAddEquitySell and LastAddEquityBuy by <amount>, and prompts to restart.

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
  if docker compose version >/dev/null 2>&1; then
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

# Minimal change: write via temp, set ownership (65532:65532), then mv to preserve original ownership for the bot user.
edit_json_inplace() {
  local file="$1" expr="$2" tmp
  tmp="$(mktemp "${file}.XXXX")"
  jq "${expr}" "$file" >"$tmp"
  chown 65532:65532 "$tmp"
  mv -f "$tmp" "$file"
}

# Helper to count lots (treats null/missing as zero)
lots_count() {
  local file="$1" side="$2"
  jq -r "((.${side}.lots // []) | length)" "$file"
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

# Capture pre-edit lots counts for sanity check (minimal change to enforce restart safety)
PRE_BUY_LOTS="$(lots_count "$STATE_FILE" "BookBuy")"
PRE_SELL_LOTS="$(lots_count "$STATE_FILE" "BookSell")"

# --- Stop service ---
echo "Stopping service: ${COMPOSE_SVC}"
pushd "$COMPOSE_DIR" >/dev/null
$COMPOSE_BIN stop "$COMPOSE_SVC"
popd >/dev/null

# --- Apply LastAddEquitySell += AMOUNT and LastAddEquityBuy += AMOUNT ---
echo "Updating LastAddEquitySell and LastAddEquityBuy by +${AMOUNT} in ${STATE_FILE}"
cp -a "$STATE_FILE" "${STATE_FILE}.bak.$(date +%Y%m%d%H%M%S)"

edit_json_inplace "$STATE_FILE" \
  ".LastAddEquitySell = ((.LastAddEquitySell // 0) + (${AMOUNT}|tonumber)) |
   .LastAddEquityBuy  = ((.LastAddEquityBuy  // 0) + (${AMOUNT}|tonumber))"

NEW_EQ="$(read_json_key "$STATE_FILE" "EquityUSD" || true)"
NEW_LAES="$(read_json_key "$STATE_FILE" "LastAddEquitySell" || true)"
NEW_LAEB="$(read_json_key "$STATE_FILE" "LastAddEquityBuy"  || true)"

echo "Updated values:"
echo "  EquityUSD:           ${NEW_EQ}"
echo "  LastAddEquitySell:   ${NEW_LAES}"
echo "  LastAddEquityBuy:    ${NEW_LAEB}"

# Minimal change: re-check lots after edit; abort restart if non-zero -> zero drop detected.
POST_BUY_LOTS="$(lots_count "$STATE_FILE" "BookBuy")"
POST_SELL_LOTS="$(lots_count "$STATE_FILE" "BookSell")"

ABORT_RESTART="no"
if { [[ "$PRE_BUY_LOTS" =~ ^[1-9][0-9]*$ ]] && [[ "$POST_BUY_LOTS" == "0" ]]; } || \
   { [[ "$PRE_SELL_LOTS" =~ ^[1-9][0-9]*$ ]] && [[ "$POST_SELL_LOTS" == "0" ]]; }; then
  echo "WARNING: Detected lots drop from non-zero to zero (BookBuy: ${PRE_BUY_LOTS}→${POST_BUY_LOTS}, BookSell: ${PRE_SELL_LOTS}→${POST_SELL_LOTS})."
  echo "Aborting automatic restart to prevent strategy reset. Inspect ${STATE_FILE} and backups before starting the service."
  ABORT_RESTART="yes"
fi

# --- Prompt restart ---
if [[ "$ABORT_RESTART" == "yes" ]]; then
  echo "Service NOT restarted. Start later with:"
  echo "  (cd ${COMPOSE_DIR} && $COMPOSE_BIN start ${COMPOSE_SVC})"
  exit 0
fi

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
# v1
#!/usr/bin/env bash
set -euo pipefail

# kill any stragglers (safe if none)
pkill -f 'docker compose logs -f --no-color bot_binance' 2>/dev/null || true
pkill -f 'docker compose logs -f --no-color bot($| )'    2>/dev/null || true

mkdir -p /opt/coinbase/logs/audit
cd /home/chidi/coinbase/monitoring

# Binance
nohup bash -c '
  cd /home/chidi/coinbase/monitoring &&
  exec docker compose logs -f --no-color bot_binance |
  grep -E --line-buffered -A5 -B5 "(pyramid: .*baseline met|pyramid\.latch\.set|trail\.(activate|raise|trigger)|\[WARN\] FUNDS_EXHAUSTED|equity\.baseline\.set|lot\.take_pnl_est|runner\.assign|panic:|runtime error:|fatal error|SIGSEGV|stack trace|level=error|ERROR|FATAL|panic recovered)" \
  >> /opt/coinbase/logs/audit/binance_audit.log 2>&1
' >/dev/null 2>&1 &
disown

# Coinbase
nohup bash -c '
  cd /home/chidi/coinbase/monitoring &&
  exec docker compose logs -f --no-color bot |
  grep -E --line-buffered -A5 -B5 "(pyramid: .*baseline met|pyramid\.latch\.set|trail\.(activate|raise|trigger)|\[WARN\] FUNDS_EXHAUSTED|equity\.baseline\.set|lot\.take_pnl_est|runner\.assign|panic:|runtime error:|fatal error|SIGSEGV|stack trace|level=error|ERROR|FATAL|panic recovered)" \
  >> /opt/coinbase/logs/audit/coinbase_audit.log 2>&1
' >/dev/null 2>&1 &
disown

echo "Audit tails restarted. Logs: /opt/coinbase/logs/audit/{binance_audit.log,coinbase_audit.log}"

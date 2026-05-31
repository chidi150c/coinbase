#!/usr/bin/env bash
set -euo pipefail
cd /home/chidi/coinbase/monitoring
exec docker compose logs -f --no-color bot \
  | grep -E --line-buffered -A5 -B5 '(pyramid: .*baseline met|pyramid\.latch\.set|trail\.(activate|raise|trigger)|\[WARN\] FUNDS_EXHAUSTED|equity\.baseline\.set|lot\.take_pnl_est|runner\.assign|panic:|runtime error:|fatal error|SIGSEGV|stack trace|level=error|ERROR|FATAL|panic recovered)' \
  >> /opt/coinbase/logs/audit/coinbase_audit.log 2>&1

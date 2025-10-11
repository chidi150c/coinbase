# Special CLI Commands:

# 1) Snapshot current open lots (size/side/targets)
jq '{lots: [.Lots[] | {side: .Side, base: .SizeBase, open: .OpenPrice, stop: .Stop, take: .Take, opened: .OpenTime, reason: (.reason // "")}],
     count: (.Lots|length)}' /opt/coinbase/state/bot_state.json

jq '{lots: [.Lots[] | {side: .Side, base: .SizeBase, open: .OpenPrice, stop: .Stop, take: .Take, opened: .OpenTime, reason: (.reason // "")}],
     count: (.Lots|length)}' /opt/coinbase/state/bot_state.coinbase.json

jq '{lots: [.Lots[] | {side: .Side, base: .SizeBase, open: .OpenPrice, stop: .Stop, take: .Take, opened: .OpenTime, reason: (.reason // "")}],
     count: (.Lots|length)}' /opt/coinbase/state/bot_state.binance.json

=========================================
# normal front
docker compose logs -f --since "15m" bot \
| GREP_COLOR='01;32' grep --line-buffered -E --color=always 'MA Signalled|Decision=(BUY|SELL)|LIVE ORDER|^PAPER|^EXIT|reason=|entry_reason=|$' \
| GREP_COLOR='01;36' grep --line-buffered -E --color=always 'pUp=|gatePrice=|latched=|effPct=|basePct=|elapsedHr=|HighPeak=|PriceDownGoingUp=|LowBottom=|PriceUpGoingDown=|$' \
| GREP_COLOR='01;31' grep --line-buffered -E --color=always 'pyramid: blocked|GATE (BUY|SELL)|partial fill|commission missing|ERR step|$'

==========================================

# One-time backup service run
sudo /usr/local/sbin/coinbase_state_backup.sh
ls -lh /opt/coinbase/state/backup | tail -n 5

=====================================

# Restore (when needed)
gunzip -c /opt/coinbase/state/backup/bot_state.latest.json.gz > /opt/coinbase/state/bot_state.json
docker compose restart bot
====================================
# To switch back to Coinbase via the bridge
# replace existing broker line (or add it if missing)
sudo sed -i 's/^BROKER=.*/BROKER=bridge/' /opt/coinbase/env/bot.env 
docker compose up -d --force-recreate bot

===========================================
# Revert to Binance 
# Change back:
sudo sed -i 's/^BROKER=.*/BROKER=binance/' /opt/coinbase/env/bot.env
docker compose up -d --force-recreate bot

===========================================
# To verify switching
# confirm the env
docker inspect $(docker compose ps -q bot) \
  --format '{{range .Config.Env}}{{println .}}{{end}}' | grep -E 'BRIDGE_URL|BROKER='

# watch logs — you should no longer see Binance endpoints like /api/v3/order
docker compose logs -f --since "2m" bot | grep -E 'LIVE ORDER|EXIT|Decision'

# Verify Binance
docker inspect "$(docker compose ps -q bot)" \
  --format '{{range .Config.Env}}{{println .}}{{end}}' \
  | grep -E '^BROKER=|^BINANCE_(API_KEY|API_SECRET|API_BASE|USE_TESTNET|RECV_WINDOW_MS)='

===============================================

docker compose up -d bot bot_binance

# Coinbase: should show no Binance endpoints
docker compose logs -f --since "1m" bot | grep -E '/api/v3|binance' || echo "OK: Coinbase-only"

# Binance: watch for order/auth/time
docker compose logs -f --since "2m" bot_binance | grep -E '/api/v3/order|-2014|-1021|LIVE ORDER|EXIT'


===============================================
docker compose logs -f --since "15m" bot_hitbtc \
| GREP_COLOR='01;32' grep --line-buffered -E --color=always 'MA Signalled|Decision=(BUY|SELL)|LIVE ORDER|^PAPER|^EXIT|reason=|entry_reason=|$' \
| GREP_COLOR='01;36' grep --line-buffered -E --color=always 'pUp=|gatePrice=|latched=|effPct=|basePct=|elapsedHr=|HighPeak=|PriceDownGoingUp=|LowBottom=|PriceUpGoingDown=|$' \
| GREP_COLOR='01;35' grep --line-buffered -E --color=always 'TRACE exit\.classify|$' \
| GREP_COLOR='01;31' grep --line-buffered -E --color=always 'lot cap reached|pyramid: blocked|GATE (BUY|SELL)|partial fill|commission missing|ERR step|$'




docker compose logs -f --since "15m" bot_binance \
| GREP_COLOR='01;32' grep --line-buffered -E --color=always 'MA Signalled|Decision=(BUY|SELL)|LIVE ORDER|^PAPER|^EXIT|reason=|entry_reason=|$' \
| GREP_COLOR='01;36' grep --line-buffered -E --color=always 'pUp=|gatePrice=|latched=|effPct=|basePct=|elapsedHr=|HighPeak=|PriceDownGoingUp=|LowBottom=|PriceUpGoingDown=|$' \
| GREP_COLOR='01;35' grep --line-buffered -E --color=always 'TRACE exit\.classify|$' \
| GREP_COLOR='01;31' grep --line-buffered -E --color=always 'lot cap reached|pyramid: blocked|GATE (BUY|SELL)|partial fill|commission missing|ERR step|$'



docker compose logs -f --since "15m" bot \
| GREP_COLOR='01;32' grep --line-buffered -E --color=always 'MA Signalled|Decision=(BUY|SELL)|LIVE ORDER|^PAPER|^EXIT|reason=|entry_reason=|$' \
| GREP_COLOR='01;36' grep --line-buffered -E --color=always 'pUp=|gatePrice=|latched=|effPct=|basePct=|elapsedHr=|HighPeak=|PriceDownGoingUp=|LowBottom=|PriceUpGoingDown=|$' \
| GREP_COLOR='01;35' grep --line-buffered -E --color=always 'TRACE exit\.classify|$' \
| GREP_COLOR='01;31' grep --line-buffered -E --color=always 'lot cap reached|pyramid: blocked|GATE (BUY|SELL)|partial fill|commission missing|ERR step|$'



=====================================================
  xxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxx
# Show both; you already saw IPv6, grab IPv4 too:
curl -6 -s https://ifconfig.me ; echo
curl -4 -s https://ifconfig.me ; echo   # <-- whitelist this IPv4 on your Binance API key
2600:3c13::2000:e7ff:fe3b:33b5
172.236.14.121
  xxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxx
=======================================================
# Reset both working tree and index to the last commit
git restore --source=HEAD --staged --worktree .
# or (older Git)
git checkout -- .
git reset --hard

=======================================================

# from monitoring/ directory
docker compose build --pull --no-cache bridge_binance bridge_hitbtc
docker compose up -d --force-recreate bridge_binance bridge_hitbtc

# confirm new image build times/IDs
docker compose images

===============================================================
# One-time manual deep clean (careful—removes ALL unused stuff):
docker system prune -a -f --volumes
==============================================================
# Confirm the exit PnL trace is live
docker compose logs bot --since 30m | grep 'TRACE exit.classify' || true

docker compose logs bot_binance --since 30m | grep 'TRACE exit.classify' || true

============================================================

# start tailers (run from the compose directory)
nohup bash -c 'docker compose logs -f --no-color bot_binance >> /opt/coinbase/logs/bot_binance.log 2>&1' &
nohup bash -c 'docker compose logs -f --no-color bot_hitbtc  >> /opt/coinbase/logs/bot_hitbtc.log  2>&1' &
nohup bash -c 'docker compose logs -f --no-color bot         >> /opt/coinbase/logs/bot_coinbase.log 2>&1' &

=========================================================================================================
# Capturing into logs for later veiwing 
mkdir -p /opt/coinbase/logs/audit

# Binance
nohup bash -c '
  cd /home/chidi/coinbase/monitoring &&
  exec docker compose logs -f --no-color bot_binance |
  grep -E --line-buffered -A5 -B5 "(pyramid: .*baseline met|pyramid\.latch\.set|trail\.(activate|raise|trigger)|\[WARN\] FUNDS_EXHAUSTED|equity\.baseline\.set|lot\.take_pnl_est|runner\.assign|panic:|runtime error:|fatal error|SIGSEGV|stack trace|level=error|ERROR|FATAL|panic recovered)" \
  >> /opt/coinbase/logs/audit/binance_audit.log 2>&1
' >/dev/null 2>&1 &
disown

# HitBTC
nohup bash -c \
"docker compose logs -f --no-color bot_hitbtc \
 | grep -E --line-buffered 'pyramid\.baseline\.met|pyramid\.latch\.set|trail\.(activate|raise|trigger)|\[WARN\] FUNDS_EXHAUSTED' \
 >> /opt/coinbase/logs/audit/hitbtc_audit.log 2>&1" &

# Coinbase
nohup bash -c '
  cd /home/chidi/coinbase/monitoring &&
  exec docker compose logs -f --no-color bot |
  grep -E --line-buffered -A5 -B5 "(pyramid: .*baseline met|pyramid\.latch\.set|trail\.(activate|raise|trigger)|\[WARN\] FUNDS_EXHAUSTED|equity\.baseline\.set|lot\.take_pnl_est|runner\.assign|panic:|runtime error:|fatal error|SIGSEGV|stack trace|level=error|ERROR|FATAL|panic recovered)" \
  >> /opt/coinbase/logs/audit/coinbase_audit.log 2>&1
' >/dev/null 2>&1 &
disown

# Here are handy one-liners you can stash and reuse (all work with your audit-grep):

# Core patterns (Coinbase)

# Trailing activity with context:

audit-grep coinbase 'trail\.(activate|raise|trigger)' -n -A2 -B2


# Funding + min-notional skips (highlighted):

audit-grep coinbase 'FUNDS_EXHAUSTED|CLOSE-SKIP' -n --color

# Runner assignment + TP/PNL estimates:

audit-grep coinbase 'runner\.assign|lot\.take_pnl_est' -n -A1


# Pyramid gates (baseline met + latch set):

audit-grep coinbase 'pyramid: .*baseline met|pyramid\.latch\.set' -n -B1

# Equity baseline updates:

audit-grep coinbase 'equity\.baseline\.set' -n

# Errors / crashes

# Hard errors & panics with 5 lines of context:

audit-grep coinbase 'panic:|runtime error:|fatal error|SIGSEGV|stack trace|level=error|ERROR|FATAL|panic recovered' -n -A5 -B5 --color

# Side/exit specifics

# Exit reasons (take_profit / stop_loss / trailing_stop):

audit-grep coinbase 'EXIT .*reason=(take_profit|stop_loss|trailing_stop)' -n

# Close-skip with notional details:

# audit-grep coinbase '\[CLOSE-SKIP\].*notional=.*< ORDER_MIN_USD' -n

# Time-scoped quick filters (rough by minute)

# Look at a specific minute (e.g., 2025-10-10T09:45):

audit-grep coinbase '2025-10-10T09:45' -n

# Case-insensitive scans / summaries

# Case-insensitive scan for “hold” decisions with counts:

audit-grep coinbase '^\S+ HOLD$' -ni | wc -l


# Count each exit reason frequency:

audit-grep coinbase 'EXIT .*reason=' -n \
  | sed -n 's/.*reason=\(\w\+\).*/\1/p' \
  | sort | uniq -c | sort -nr

# Last-N style peeks

# Show last 50 trailing events across archives:

audit-grep coinbase 'trail\.(activate|raise|trigger)' -n | tail -50


# Last 100 equity/PNL signals:

audit-grep coinbase 'equity\.baseline\.set|lot\.take_pnl_est' -n | tail -100

# Swap service target

# Just replace coinbase with binance or hitbtc, e.g.:

audit-grep binance 'FUNDS_EXHAUSTED|CLOSE-SKIP' -n --color
audit-grep hitbtc  'runner\.assign|lot\.take_pnl_est' -n -A1
===============================================================================
# BACK UP RESTORE
# 0) Paths (adjust if yours differ)
STATE_DIR="/opt/coinbase/state"
STATE="${STATE_DIR}/bot_state.newcoinbase.json"

# 1) Stop the service before touching state
cd /home/chidi/coinbase/monitoring
docker compose stop bot

# 2) See what backups you have (script-made backups look like *.bak.YYYYMMDDHHMMSS)
ls -lt ${STATE}.bak* 2>/dev/null || true
# If none show up, list other probable backups you had earlier:
ls -lt ${STATE_DIR}/bot_state.*coinbase*.json* 2>/dev/null || true
ls -lt ${STATE_DIR}/backup/ 2>/dev/null || true

# 3) Pick the backup you want to restore (EDIT THIS to the correct file from the listing)
BK="$(ls -t ${STATE}.bak.* 2>/dev/null | head -n1)"

# If the above didn't find one, manually set BK to a known-good backup, e.g.:
# BK="/opt/coinbase/state/bot_state.coinbase.json.bak"
# BK="/opt/coinbase/state/bot_state.coinbase.json.pre-restore.2025-09-29T19:51:33-05:00"

# Make sure BK is set
[[ -n "$BK" && -r "$BK" ]] || { echo "Backup not found or unreadable: $BK"; exit 1; }
echo "Restoring from: $BK"

# 4) Quick sanity on backup (lots present? keys look right?)
jq -r '.EquityUSD, .LastAddEquitySell, .LastAddEquityBuy' "$BK"
jq -r '((.BookBuy.lots // [])|length) as $b | ((.BookSell.lots // [])|length) as $s | "BookBuy.lots=\($b)  BookSell.lots=\($s)"' "$BK"

# 5) Snapshot the CURRENT (bad) file before overwrite
sudo cp -a "$STATE" "${STATE}.bad.$(date +%Y%m%d%H%M%S)"

# 6) Restore the backup, ensuring ownership 65532:65532
sudo install -m 0644 -o 65532 -g 65532 "$BK" "$STATE"

# 7) Verify restored values
jq -r '.EquityUSD, .LastAddEquitySell, .LastAddEquityBuy' "$STATE"
jq -r '((.BookBuy.lots // [])|length) as $b | ((.BookSell.lots // [])|length) as $s | "BookBuy.lots=\($b)  BookSell.lots=\($s)"' "$STATE"

# 8) If everything looks correct, start the service again
docker compose start bot

# 9) Watch it come up
docker compose logs -f --no-color bot | sed -u -n '1,120p'
================================================================================================




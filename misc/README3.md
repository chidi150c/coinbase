# Special CLI Commands:

# 1) Snapshot current open lots (size/side/targets)
jq '{lots: [.Lots[] | {side: .Side, base: .SizeBase, open: .OpenPrice, stop: .Stop, take: .Take, opened: .OpenTime, reason: (.reason // "")}],
     count: (.Lots|length)}' /opt/coinbase/state/bot_state.json

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





  docker compose logs -f --since "15m" bot_binance | GREP_COLOR='01;32' grep --line-buffered -E --color=always 'MA Signalled|Decision=(BUY|SELL)|LIVE ORDER|^PAPER|^EXIT|reason=|entry_reason=|$' | GREP_COLOR='01;36' grep --line-buffered -E --color=always 'pUp=|gatePrice=|latched=|effPct=|basePct=|elapsedHr=|HighPeak=|PriceDownGoingUp=|LowBottom=|PriceUpGoingDown=|$' | GREP_COLOR='01;31' grep --line-buffered -E --color=always 'pyramid: blocked|GATE (BUY|SELL)|partial fill|commission missing|ERR step|$'


    docker compose logs -f --since "15m" bot | GREP_COLOR='01;32' grep --line-buffered -E --color=always 'MA Signalled|Decision=(BUY|SELL)|LIVE ORDER|^PAPER|^EXIT|reason=|entry_reason=|$' | GREP_COLOR='01;36' grep --line-buffered -E --color=always 'pUp=|gatePrice=|latched=|effPct=|basePct=|elapsedHr=|HighPeak=|PriceDownGoingUp=|LowBottom=|PriceUpGoingDown=|$' | GREP_COLOR='01;31' grep --line-buffered -E --color=always 'pyramid: blocked|GATE (BUY|SELL)|partial fill|commission missing|ERR step|$'

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




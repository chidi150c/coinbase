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




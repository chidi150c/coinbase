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


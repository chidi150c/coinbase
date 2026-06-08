



=========================================================

grep -E "\[KPI\] logic.route.*logic=(BUY|SELL)|ai_FLAT_logicOpinion=(BUY|SELL)_allowed|TRACE sizing.confidence|TRACE postonly.pending.set|TRACE tp.arm|TRACE postonly.exit|\[KPI\] maker.exit" \
/opt/coinbase/logs/audit/binance_audit.log | tail -100

========================================================

 docker compose logs -f --since "15m" bot_binance | GREP_COLOR='01;32' grep --line-buffered -E --color=always 'MODEL_FIT|MODEL]|DATASET|MA Signalled|Decision=(BUY|SELL|FLAT)|LIVE ORDER|^PAPER|^EXIT|reason=|entry_reason=|$' | GREP_COLOR='01;36' grep --line-buffered -E --color=always 'pUp=|macdHist=|macdDelta=|ema2050Spread=|ema20Slope=|ema50Slope=|gatePrice=|latched=|effPct=|basePct=|elapsedHr=|HighPeak=|PriceDownGoingUp=|LowBottom=|PriceUpGoingDown=|$' | GREP_COLOR='01;35' grep --line-buffered -E --color=always 'TRACE exit\.classify|$' | GREP_COLOR='01;31' grep --line-buffered -E --color=always 'lot cap reached|pyramid: blocked|GATE (BUY|SELL)|partial fill|commission missing|ERR step|$'

=========================================================
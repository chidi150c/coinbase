Coinbase Advanced Trade Bot (Go) + Bridge (FastAPI) + Monitoring Stack
1) Repository Layout (~/coinbase)
~/coinbase
├── README.md
├── backtest.go
├── bot.log
├── bot.pid
├── bridge/
│   ├── Dockerfile
│   ├── __pycache__/
│   ├── app.py
│   └── requirements.txt
├── broker.go
├── broker_bridge.go
├── broker_paper.go
├── config.go
├── data/
│   └── BTC-USD.csv
├── env.go
├── go.mod
├── go.sum
├── indicators.go
├── live.go
├── main.go
├── metrics.go
├── model.go
├── monitoring/
│   ├── alertmanager/
│   ├── docker-compose.yml
│   ├── grafana/
│   ├── grafana-data/
│   └── prometheus/
├── strategy.go
├── tools/
│   └── backfill_bridge_paged.go
├── trader.go
└── verify.txt

2) Environment Files (host path: /opt/coinbase/env/)

/opt/coinbase/env/bot.env

# === Trading target ===
PRODUCT_ID=BTC-USD
GRANULARITY=ONE_MINUTE

# === Safety (Go bot) ===
DRY_RUN=false
LONG_ONLY=true
ORDER_MIN_USD=5.00
MAX_DAILY_LOSS_PCT=1.0
RISK_PER_TRADE_PCT=0.25
USD_EQUITY=1000.00
TAKE_PROFIT_PCT=0.20
STOP_LOSS_PCT=0.20

# === Strategy tunables ===
BUY_THRESHOLD=0.47
SELL_THRESHOLD=0.50
USE_MA_FILTER=false
BACKTEST_SLEEP_MS=500

# === Ops ===
PORT=8080
BRIDGE_URL=http://bridge:8787
USE_LIVE_EQUITY=true

# Phase-7 (all optional; defaults keep baseline)
MODEL_MODE=extended        # baseline | extended
WALK_FORWARD_MIN=60        # 0 disables; 60 = hourly refit in live
VOL_RISK_ADJUST=true       # shrink risk in high vol
# SLACK_WEBHOOK=https://hooks.slack.com/services/XXX/YYY/ZZZ  # optional


(Optional toggle used by live.go: USE_TICK_PRICE=true to nudge the last candle from /price, default absent/false.)

/opt/coinbase/env/bridge.env

# === Coinbase API (used by Python sidecar) ===
COINBASE_API_KEY_NAME=organizations/.../apiKeys/...
COINBASE_API_PRIVATE_KEY=-----BEGIN EC PRIVATE KEY-----\n...\n-----END EC PRIVATE KEY-----\n
COINBASE_API_BASE=https://api.coinbase.com

# === Bridge runtime ===
PORT=8787


(Optional WebSocket toggles supported by bridge/app.py:
COINBASE_WS_ENABLE=true, COINBASE_WS_PRODUCTS=BTC-USD[,ETH-USD,...],
COINBASE_WS_URL=wss://advanced-trade-ws.coinbase.com, COINBASE_WS_STALE_SEC=10.)

3) Services & Networking (~/coinbase/monitoring/docker-compose.yml)

Services (observed via docker compose ps):

bot (image: golang:1.23) — internal port 8080/tcp (Prometheus target); not published to host by default.

bridge (image: coinbase-bridge:curl) — internal port 8787/tcp (FastAPI).

prometheus (prom/prometheus:latest) — host port 9090 → container 9090.

alertmanager (prom/alertmanager:latest) — host port 9093 → container 9093.

grafana (grafana/grafana:latest) — host port 3000 → container 3000.

Networking:

Prometheus scrapes bot:8080/metrics.

Bridge and Bot are reachable by service DNS (bridge, bot) from within the compose network.

4) Go Bot (core)

Entry & HTTP server:

main.go starts the HTTP server on cfg.Port (env PORT, default 8080).

Endpoints:

GET /healthz → "ok\n"

GET /metrics → Prometheus exposition

Core files & roles:

env.go — environment loader helpers (loadBotEnv, getEnv*).

config.go — Config struct; loads knobs from env.

indicators.go — SMA, RSI, ZScore (+ rolling helpers).

model.go — tiny logistic model (pUp).

strategy.go — Candle, Signal, Decision, decide(...) (micro-model + thresholds + optional MA regime).

trader.go — synchronized trading loop (step), position state, circuit breaker, risk sizing, stops/takes, metrics updates, optional Slack pings, volatility-aware risk (opt-in) 
.

live.go — live runner: warmup (~300 candles), model fit, polling cadence, optional live equity rebasing, opt-in tick-price nudging from bridge /price, and walk-forward refit (env-driven minutes) 
.

metrics.go — Prometheus metrics (existing) plus appended non-breaking series for Phase-7 
.

broker.go — broker interface; broker_bridge.go calls the Python bridge; broker_paper.go simulates fills in DryRun.

backtest.go — CSV loader + simple backtest (70/30 split, warmup=50, pacing via BACKTEST_SLEEP_MS).

data/BTC-USD.csv — ~6000 rows, 1-minute.

tools/backfill_bridge_paged.go — paged backfill via bridge /candles.

Bot runtime knobs (selected):

Trading target: PRODUCT_ID, GRANULARITY.

Safety: DRY_RUN, LONG_ONLY, ORDER_MIN_USD, MAX_DAILY_LOSS_PCT, RISK_PER_TRADE_PCT, USD_EQUITY, TAKE_PROFIT_PCT, STOP_LOSS_PCT.

Thresholds: BUY_THRESHOLD, SELL_THRESHOLD, USE_MA_FILTER.

Ops: PORT, BRIDGE_URL, USE_LIVE_EQUITY.

Phase-7 (opt-ins): MODEL_MODE=baseline|extended, WALK_FORWARD_MIN (integer minutes, 0 disables), VOL_RISK_ADJUST (bool), SLACK_WEBHOOK (URL), and USE_TICK_PRICE (bool; optional).

Key live features (opt-in; do not change defaults):

Live equity rebasing from bridge /accounts when USE_LIVE_EQUITY=true (otherwise static equity). 

Walk-forward refit every WALK_FORWARD_MIN minutes when MODEL_MODE=extended (counts bot_walk_forward_fits_total). 
 

Tick-price nudge: when USE_TICK_PRICE=true, fetch /price and update last candle’s OHLC to react intra-interval (no behavior change unless enabled). 

Volatility-aware risk: when VOL_RISK_ADJUST=true, compute a multiplicative factor and apply to RISK_PER_TRADE_PCT; exposes bot_vol_risk_factor. 
 

Optional Slack pings: if SLACK_WEBHOOK is set, best-effort POSTs on entries/exits/errors/circuit-breaker; never affects trading flow. 

5) Python Bridge (FastAPI) — ~/coinbase/bridge/app.py

Auth & client:

Uses COINBASE_API_KEY_NAME and COINBASE_API_PRIVATE_KEY (or COINBASE_API_SECRET).

COINBASE_API_BASE=https://api.coinbase.com

PORT=8787.

HTTP endpoints (JSON):

GET /health → {"ok": true}

GET /accounts?limit= — returns Coinbase accounts (SDK .to_dict()).

GET /product/{product_id} — product object (price fields may vary).

GET /candles?product_id=&granularity=&limit=&start=&end= — normalized OHLCV (strings); supports SDK first and HTTP fallback with UNIX seconds (limit ≤ 350).

POST /orders/market_buy — consistent with Coinbase SDK.

POST /order/market — unified market order by quote_size for BUY/SELL; SELL falls back to base_size using current price if SDK requires it.

GET /price?product_id= — latest price from in-memory WS cache (below); returns {"product_id","price","ts","stale"}.

WebSocket ticker (optional; opt-in via env):

COINBASE_WS_ENABLE=true enables background task that connects to wss://advanced-trade-ws.coinbase.com, subscribes to ticker for COINBASE_WS_PRODUCTS (comma-separated), and updates _last_ticks[product_id] = {price, ts}.

COINBASE_WS_STALE_SEC (default 10) marks /price as stale if no fresh tick in that window.

If WS isn’t enabled or no tick yet, /price returns {"error":"no_tick", "product_id": ...}.

6) Metrics & Monitoring

Prometheus (port 9090):

Scrape target bot:8080/metrics every 15s (standard text exposition).

Core metrics:

bot_orders_total{mode="paper|live",side="BUY|SELL"}

bot_decisions_total{signal="buy|sell|flat"}

bot_equity_usd

Appended (non-breaking) metrics (Phase-7) 
:

bot_model_mode{mode="baseline|extended"} — gauges toggled 0/1 by code.

bot_vol_risk_factor — current volatility risk multiplier.

bot_walk_forward_fits_total — counter of walk-forward model refits.

Prometheus rules (in monitoring/prometheus/rules.yml):

BotDown: up{job="coinbase-bot"} == 0 for 2m (severity=critical).

NoDecisionsRecently: increase(bot_decisions_total[30m]) == 0 for 30m (severity=warning).

EquityDropRapid: (max_over_time(bot_equity_usd[1h]) - bot_equity_usd)/clamp_min(max_over_time(bot_equity_usd[1h]),0.0001) > 0.01 for 5m (severity=critical).

Alertmanager (port 9093):

Route: receiver=slack, group_by=[alertname], group_wait=10s, group_interval=2m, repeat_interval=2h.

Receiver slack: api_url=https://hooks.slack.com/services/..., send_resolved=true.

Grafana (port 3000):

Admin creds: admin / admin (from compose; can be changed).

Dashboard JSON (~/coinbase/monitoring/grafana/bot-dashboard.json) includes:

Timeseries “Equity (USD)” → bot_equity_usd

Timeseries “Decisions (buy/sell/flat)” → increase(bot_decisions_total{signal="buy"}[5m]), same for sell/flat

Timeseries “Orders Placed (BUY vs SELL)” → increase(bot_orders_total{mode="live",side="BUY"}[1h]), same for SELL

Stat “Circuit Breaker Status” → clamp_max(sum(increase(bot_decisions_total{signal="flat"}[5m])), 1) - clamp_max(sum(increase(bot_orders_total{mode="live"}[5m])), 1)

Stat “Total Trades” → sum(bot_orders_total{mode="live"})

Timeseries “Daily P/L (USD)” → bot_equity_usd - (bot_equity_usd offset 1d)

7) HTTP Surfaces

Bot (internal, port 8080):

GET /healthz

GET /metrics

Bridge (internal, port 8787):

GET /health

GET /accounts?limit=

GET /product/{product_id}

GET /candles?product_id=&granularity=&limit=&start=&end=

POST /orders/market_buy

POST /order/market

GET /price?product_id=

8) Runbooks / Commands

Start monitoring stack:

cd ~/coinbase/monitoring
docker compose up -d


Health checks (from bot container namespace):

docker compose exec bot sh -lc 'curl -s http://bridge:8787/health'
docker compose exec bot sh -lc 'curl -s http://localhost:8080/healthz'


Metrics quick peek:

docker compose exec bot sh -lc 'curl -s http://localhost:8080/metrics | egrep -m1 "bot_equity_usd|bot_decisions_total|bot_orders_total"'


Prometheus target check (host):

curl -s http://localhost:9090/api/v1/targets


Grafana dashboard import (host):

cd ~/coinbase/monitoring/grafana
curl -X POST -H "Content-Type: application/json" -u admin:admin \
  http://localhost:3000/api/dashboards/db -d @bot-dashboard.json


Backfill CSV via bridge (paged):

docker compose run --rm bot go run /app/tools/backfill_bridge_paged.go \
  -product BTC-USD -granularity ONE_MINUTE -limit 300 -pages 20 -out /app/data/BTC-USD.csv


Backtest (paper mode forced by code path):

docker compose run --rm bot /usr/local/go/bin/go run . \
  -backtest /app/data/BTC-USD.csv -interval 1


Live trading:

Ensure DRY_RUN=false and BRIDGE_URL=http://bridge:8787 in /opt/coinbase/env/bot.env.

Bot service (compose) runs /usr/local/go/bin/go run . -live -interval 15 (typical cadence).

Kill-switch:

Set DRY_RUN=true in /opt/coinbase/env/bot.env and docker compose restart bot, or:

docker compose stop bot.

9) Safety & Operational Notes

Circuit breaker: MAX_DAILY_LOSS_PCT enforced in trader.go to suspend new entries after daily drawdown.

Long-only: LONG_ONLY=true prevents new SELL entries when flat (spot constraint).

Order floor: ORDER_MIN_USD ensures minimum notional.

Stops/Takes: STOP_LOSS_PCT, TAKE_PROFIT_PCT applied per position.

Dry-run vs live: DRY_RUN controls whether real orders are sent via bridge; bot_orders_total{mode="paper|live"} captures counts accordingly.

Optional Slack: SLACK_WEBHOOK (if set) posts best-effort notifications; failures never block trading. 

Optional tick use: USE_TICK_PRICE=true enables /price-driven intra-bar updates; default is off. 
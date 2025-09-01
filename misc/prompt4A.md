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
├── strategy.go
├── trader.go
├── tools/
│   └── backfill_bridge_paged.go
├── misc/
│   ├── README.md
│   ├── issues.md
│   ├── prompt1.md
│   ├── prompt2.md
│   ├── prompt4A.md
│   ├── prompt5.md
│   ├── prompt5B.md
│   ├── prompt6.md
│   ├── prompt7_Change_Description.md
│   ├── prompt7_Change_Proccess.md
│   └── promtp7_Change_Tiny.md
├── monitoring/
│   ├── alertmanager/
│   ├── docker-compose.yml
│   ├── grafana/
│   ├── grafana-data/
│   └── prometheus/
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
FEE_RATE_PCT=0.1
MAX_HISTORY_CANDLE=5000

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
MODEL_MODE=extended
WALK_FORWARD_MIN=60
VOL_RISK_ADJUST=true
# SLACK_WEBHOOK=https://hooks.slack.com/services/XXX/YYY/ZZZ


Optional:

USE_TICK_PRICE=true (live tick nudging)

Pyramiding: ALLOW_PYRAMIDING, PYRAMID_MIN_SECONDS_BETWEEN, PYRAMID_MIN_ADVERSE_PCT

/opt/coinbase/env/bridge.env
# === Coinbase API ===
COINBASE_API_KEY_NAME=organizations/.../apiKeys/...
COINBASE_API_PRIVATE_KEY=-----BEGIN EC PRIVATE KEY-----\n...\n-----END EC PRIVATE KEY-----\n
COINBASE_API_BASE=https://api.coinbase.com

# === Bridge runtime ===
PORT=8787


Optional WS toggles:

COINBASE_WS_ENABLE=true

COINBASE_WS_PRODUCTS=BTC-USD[,ETH-USD,...]

COINBASE_WS_URL=wss://advanced-trade-ws.coinbase.com

COINBASE_WS_STALE_SEC=10

3) Services & Networking (~/coinbase/monitoring/docker-compose.yml)
bot

Image: golang:1.23

Working dir: /app

Volumes:

..:/app

/opt/coinbase/env:/opt/coinbase/env:ro

Env file: /opt/coinbase/env/bot.env

Command: /usr/local/go/bin/go run . -live -interval 1

Expose: 8080

Restart: unless-stopped

Logging: json-file (max-size=10m, max-file=5)

Networks: alias bot, coinbase-bot

bridge

Image: built from ../bridge/Dockerfile (coinbase-bridge:curl)

Working dir: /app/bridge

Volumes:

../bridge:/app/bridge:ro

/opt/coinbase/env:/opt/coinbase/env:ro

Env file: /opt/coinbase/env/bridge.env

Expose: 8787

Restart: unless-stopped

Networks: alias bridge

prometheus

Image: prom/prometheus:latest

Ports: 9090:9090

Volumes:

./prometheus/prometheus.yml:/etc/prometheus/prometheus.yml

monitoring_prometheus_data:/prometheus

Restart: unless-stopped

alertmanager

Image: prom/alertmanager:latest

Ports: 9093:9093

Volumes:

./alertmanager/alertmanager.yml:/etc/alertmanager/alertmanager.yml:ro

grafana

Image: grafana/grafana:latest

Ports: 3000:3000

Env:

GF_SECURITY_ADMIN_USER=admin

GF_SECURITY_ADMIN_PASSWORD=admin

Volumes: monitoring_grafana_data:/var/lib/grafana

Depends on: prometheus, bot, bridge

Networks

Single: monitoring_network (bridge driver)

Volumes

monitoring_prometheus_data

monitoring_grafana_data

4) Go Bot (core)

env.go → loadBotEnv() only loads whitelisted keys.

config.go → Config struct (all trading knobs), extended toggles, FeeRatePct, MaxHistoryCandle.

indicators.go → SMA, RSI, ZScore.

model.go → logistic model baseline/extended.

strategy.go → thresholds (BUY/SELL), optional MA filter.

trader.go:

Lots, pyramiding, TP/SL

Fees deducted with FeeRatePct

Equity + PnL updates

Circuit breaker (MAX_DAILY_LOSS_PCT)

Metrics:

bot_decisions_total{signal}

bot_orders_total{mode,side}

bot_trades_total{result=open|win|loss}

bot_equity_usd

bot_model_mode{mode}

bot_vol_risk_factor

bot_walk_forward_fits_total

live.go:

Tick loop

Candle resync every CANDLE_RESYNC_SEC

Bootstrap grows up to MAX_HISTORY_CANDLE (default 5000)

Walk-forward refits by WALK_FORWARD_MIN

Tick nudging from /price when USE_TICK_PRICE=true

Live equity rebasing from /accounts

metrics.go:

Prometheus exposition at :8080/metrics.

5) Backtest (backtest.go)

Loads CSV (data/BTC-USD.csv)

Supports ~6000 1m candles (can extend to 5000–6000+)

Splits into train/test (70/30 default; configurable)

Warmup: 50

Backtest pacing via BACKTEST_SLEEP_MS

Model fit across expanded history

DRY_RUN disables live order placement

Metrics update identically to live mode

6) Python Bridge (FastAPI) — bridge/app.py

Endpoints:

/health

/accounts?limit=

/product/{product_id}

/candles?granularity&limit&product_id (limit ≤ 350)

/orders/market_buy

/order/market (BUY/SELL by quote size)

/price?product_id=

WebSocket:

Updates _last_ticks for products if enabled.

/price returns latest tick or stale.

7) Metrics & Monitoring

Prometheus scrapes bot:8080/metrics.

Metrics:

bot_orders_total{mode,side}

bot_decisions_total{signal}

bot_equity_usd

bot_model_mode{mode}

bot_vol_risk_factor

bot_walk_forward_fits_total

bot_trades_total{result=open|win|loss}

Grafana dashboard:

Equity curve, trades, MA overlays

Circuit breaker status

PnL/daily changes

Volatility factor

Alertmanager:

Slack integration

Alerts on downtime, 0-decisions, equity drop.

8) HTTP Surfaces

Bot (8080):

/healthz

/metrics

Bridge (8787):

/health

/accounts

/product/{id}

/candles

/price

/orders/market_buy

/order/market

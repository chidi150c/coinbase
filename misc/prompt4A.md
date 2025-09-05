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


Integrated details (from Text B):

/opt/coinbase/state/bot_state.json — persisted bot state file (path set by STATE_FILE) used by trader.go (see §4).

trader.go defines:

Position state (open price/side/size/stop/take/fees/trailing)

Trader orchestration (config, broker, model, equity/PnL, persisted state)

The synchronized step() loop for OPEN/HOLD/EXIT

Pyramiding (adds), per-add scalp TP decay, and a “runner” lot with optional trailing

Runtime logs include [TICK] and [DEBUG] lines; broker interface includes PlaceMarketQuote(...); PaperBroker supported.

Optional:

USE_TICK_PRICE=true (live tick nudging)

Pyramiding: ALLOW_PYRAMIDING, PYRAMID_MIN_SECONDS_BETWEEN, PYRAMID_MIN_ADVERSE_PCT

# === Trading target ===
PRODUCT_ID=BTC-USD
GRANULARITY=ONE_MINUTE

# === Execution cadence (ticks vs candles) ===
USE_TICK_PRICE=true
TICK_INTERVAL_SEC=1
CANDLE_RESYNC_SEC=60

# === Risk & sizing ===
ORDER_MIN_USD=5.00
MAX_DAILY_LOSS_PCT=2.0
RISK_PER_TRADE_PCT=20.0
USD_EQUITY=68.5
MAX_HISTORY_CANDLES=5000

# === Position controls ===
DRY_RUN=false
LONG_ONLY=true
ALLOW_PYRAMIDING=true
PYRAMID_MIN_ADVERSE_PCT=1.5
PYRAMID_DECAY_LAMBDA=0.02
PYRAMID_MIN_SECONDS_BETWEEN=0
MAX_CONCURRENT_LOTS=20
PYRAMID_DECAY_MIN_PCT=0.4

# --- optional: per-add TP decay for scalp lots (OFF by default) ---
SCALP_TP_DECAY_ENABLE=true
SCALP_TP_DEC_MODE=exp
SCALP_TP_DEC_PCT=0.20
SCALP_TP_DECAY_FACTOR=0.9802
SCALP_TP_MIN_PCT=1.55

# === Exits (TP/SL + runner trailing) ===
TAKE_PROFIT_PCT=1.9
STOP_LOSS_PCT=1000.00
TRAIL_ACTIVATE_PCT=1.9
TRAIL_DISTANCE_PCT=0.4

# === Fees & state ===
FEE_RATE_PCT=0.75
STATE_FILE=/opt/coinbase/state/bot_state.json

# === Strategy thresholds ===
BUY_THRESHOLD=0.45
SELL_THRESHOLD=0.55
USE_MA_FILTER=true
BACKTEST_SLEEP_MS=100

# === Ops ===
PORT=8080
BRIDGE_URL=http://bridge:8787
USE_LIVE_EQUITY=true
MODEL_MODE=extended
WALK_FORWARD_MIN=1
VOL_RISK_ADJUST=true
DAILY_BREAKER_MARK_TO_MARKET=true
# SLACK_WEBHOOK=https://hooks.slack.com/services/XXX/YYY/ZZZ


Notes (integration):

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

Integrated details (from Text B):

Ports remain: bot :8080, bridge :8787.

Prometheus scrapes bot at http://bot:8080/metrics.

4) Go Bot (core)

env.go → loadBotEnv() only loads whitelisted keys.

config.go → Config struct (all trading knobs), extended toggles, FeeRatePct, MaxHistoryCandle (legacy naming; runtime uses MAX_HISTORY_CANDLES).

indicators.go → SMA, RSI, ZScore.

model.go → logistic model baseline/extended.

strategy.go → thresholds (BUY/SELL), optional MA filter.

metrics.go → Prometheus exposition at :8080/metrics.

Core trading loop & behavior (integrated augmentations)

trader.go:

State & data structures

Position

OpenPrice float64, Side OrderSide, SizeBase float64, Stop float64, Take float64, OpenTime time.Time

EntryFee float64 (recorded at open; deducted at close)

Runner-only trailing: TrailActive bool, TrailPeak float64, TrailStop float64

BotState (persisted)

EquityUSD float64, DailyStart time.Time, DailyPnL float64, Lots []*Position

Model *AIMicroModel, MdlExt *ExtendedLogit, WalkForwardMin int, LastFit time.Time

Trader (selected)

cfg Config, broker Broker, model *AIMicroModel

lots []*Position, pos *Position (legacy representative), lastAdd time.Time

dailyStart time.Time, dailyPnL float64, equityUSD float64

mdlExt *ExtendedLogit, stateFile string, lastFit time.Time, runnerIdx int

pyramidAnchorPrice float64, pyramidAnchorTime time.Time

Synchronized tick (step())

Uses latest candles/ticks; if candle time is zero, falls back to time.Now().UTC() for daily bookkeeping.

Keeps PaperBroker price mirror synced with latest close for realistic paper fills.

Decision via decide(...) (baseline or extended head). Metrics incremented via mtxDecisions{buy|sell|flat}.

Long-only: discretionary SELL signals ignored while lots are open; exits are TP/SL (or runner trailing) only.

Opening positions & pyramiding adds

Sizing: quote = max(ORDER_MIN_USD, (RISK_PER_TRADE_PCT/100)*equity), base = quote/price.

Baseline TP/SL from TAKE_PROFIT_PCT/STOP_LOSS_PCT.

Runner: the very first lot when flat becomes runner; TP stretched 2× (runnerTPMult=2.0), SL 1×.

Scalp TP decay (if SCALP_TP_DECAY_ENABLE=true):

exp: tpPct = TAKE_PROFIT_PCT * (SCALP_TP_DECAY_FACTOR^k), floored at SCALP_TP_MIN_PCT.

linear: tpPct = TAKE_PROFIT_PCT - k*SCALP_TP_DEC_PCT, floored at SCALP_TP_MIN_PCT.

k excludes the runner (number of existing scalps).

Pyramiding gates

Spacing: time.Since(lastAdd) >= PYRAMID_MIN_SECONDS_BETWEEN.

Adverse with time-decay:

Base adverse: PYRAMID_MIN_ADVERSE_PCT vs the last entry price.

Decay: effPct = basePct * exp(-PYRAMID_DECAY_LAMBDA * elapsed_min), floored at PYRAMID_DECAY_MIN_PCT.

elapsed_min source (authoritative): time.Since(lastAdd).Minutes().

If lastAdd is zero: seed from oldest open lot’s OpenTime, else from dailyStart.

BUY add gate: price <= last_entry * (1 - effPct/100).

lastAdd update: set to time.Now().UTC() whenever a new lot is appended (drives both spacing and decay).

Cap: MAX_CONCURRENT_LOTS enforced.

Runner trailing (optional)

Activates once price reaches TRAIL_ACTIVATE_PCT profit.

Maintains trailing stop at TRAIL_DISTANCE_PCT; triggers EXIT when crossed.

EXITs

On TP/SL or trailing trigger, place market close via PlaceMarketQuote.

PnL computed with actual execution when available (Price, BaseSize), subtracting EntryFee (recorded on open) and exit fee.

Fees prefer broker CommissionUSD; fallback to FEE_RATE_PCT if missing.

Metrics: mtxTrades{win|loss} on close; mtxOrders{mode=live|paper, side} on submit.

Equity & persistence

Equity reflected in mtxPnL via SetEquityUSD.

State persisted to STATE_FILE after changes (/opt/coinbase/state/bot_state.json).

Daily reset at UTC midnight; DAILY_BREAKER_MARK_TO_MARKET=true (ops-level mark-to-market behavior) supported.

Concurrency

Mutex protects in-memory state; network I/O (orders, broker calls) occurs with the mutex released to avoid stalls.

Safety

MAX_DAILY_LOSS_PCT circuit breaker (enforced at loop level).

LONG_ONLY=true blocks SELL entries on spot.

ORDER_MIN_USD floor maintained.

Metrics (names referenced in code)

bot_decisions_total{signal} (via mtxDecisions.WithLabelValues("buy"|"sell"|"flat").Inc())

bot_orders_total{mode,side} (via mtxOrders.WithLabelValues("live"|"paper", side).Inc())

bot_trades_total{result=open|win|loss} (on open/close)

bot_equity_usd (via mtxPnL.Set(...))

bot_model_mode{mode}

bot_vol_risk_factor (via SetVolRiskFactorMetric(...))

bot_walk_forward_fits_total

Phase-7 changes (already implemented, authoritative)

Pyramiding decay timing: uses wall-clock since last add
elapsed_min = time.Since(lastAdd).Minutes() with floor at PYRAMID_DECAY_MIN_PCT using PYRAMID_DECAY_LAMBDA.
Fallback seed when lastAdd is zero: oldest lot’s OpenTime, else dailyStart.
lastAdd is set to time.Now().UTC() when appending a new lot.

Candle-time fallback: when the latest candle timestamp is zero, use time.Now().UTC() for daily bookkeeping.

Fill-aware, fee-aware PnL: prefers Price, BaseSize, and CommissionUSD from broker; logs partial fills; subtracts entry + exit fees.

Runner management: first lot becomes runner (stretched TP 2×, SL 1×). On runner close, newest remaining lot is promoted and its trailing fields reset.

5) Backtest (backtest.go)

Loads CSV (data/BTC-USD.csv)

Supports ~6000 1m candles (can extend to 5000–6000+)

Splits into train/test (70/30 default; configurable)

Warmup: 50

Backtest pacing via BACKTEST_SLEEP_MS

Model fit across expanded history

DRY_RUN disables live order placement

Metrics update identically to live mode

Integration note: current runtime configuration uses MAX_HISTORY_CANDLES (plural) for historical window sizing.

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

Prometheus scrapes bot: 8080/metrics.

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

Verification Needed (to lock this spec as invariant)

Please paste the following so I can cross-check paths, ports, metrics, and wiring against the repo and runtime:

Repo layout

git ls-files

tree -a -L 2 from the repo root

Build/runtime wiring

go.mod and (if present) go.sum

The main entrypoint file(s), e.g., cmd/*/main.go or main.go

monitoring/docker-compose.yml (already referenced, paste the file)

Any Dockerfile(s) (root and bridge/)

Any Kubernetes manifests or systemd units, if applicable

Any Makefile or run scripts (make help, ./run.sh, etc.)

Config & env

The exact deployed /opt/coinbase/env/bot.env (you already pasted this; confirm it’s the file mounted at runtime)

/opt/coinbase/env/bridge.env

Any additional env files or secrets referenced by your orchestrator

Broker/model/metrics code

Files that define the Broker interface and PaperBroker

The decide(...) implementation and model config (baseline/extended, ExtendedLogit)

The metrics definitions for mtxPnL, mtxOrders, mtxTrades, mtxDecisions, and SetVolRiskFactorMetric

The HTTP server code that binds PORT=8080 (routes/health/metrics)

State & logs

Confirmation that /opt/coinbase/state/bot_state.json is writable (container volume or host path)

A short live log snippet showing startup and a few ticks
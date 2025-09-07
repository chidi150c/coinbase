Here is baseline text A: [Coinbase Advanced Trade Bot (Go) + Bridge (FastAPI) + Monitoring Stack
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

A short live log snippet showing startup and a few ticks]
Here is update text B: [Text B (draft) — Coinbase Advanced Trade Bot (Go) + Bridge (FastAPI) + Monitoring + CI/CD
1) Repository layout (root ~/coinbase)
~/coinbase
├── README.md
├── backtest.go
├── bot.log
├── bot.pid
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
│       └── prometheus.yml
├── bridge/
│   ├── Dockerfile
│   ├── app.py
│   └── requirements.txt
├── Dockerfile
├── .dockerignore
├── Makefile
└── .github/
    └── workflows/
        ├── ci.yml
        ├── docker.yml
        └── deploy.yml


Note: we also sometimes create a local (not committed) monitoring/docker-compose.override.yml on the VM for testing (e.g., to temporarily disable healthchecks); it is not part of the repo baseline.

2) Runtime directories on the VM (outside the repo)

/opt/coinbase/env/ — holds bot.env and bridge.env (mounted read-only).

/opt/coinbase/state/ — holds persisted bot state.

Persisted state file (env-configured):
STATE_FILE=/opt/coinbase/state/bot_state.json

3) Bot (Go, HTTP :8080, Prometheus /metrics)

Entry: main.go → live mode loop (-live -interval 1) using bridge for market data & orders.
HTTP: :8080 exposes /healthz and /metrics (Prometheus).
Metrics (names stable):

bot_orders_total{mode,side}

bot_decisions_total{signal}

bot_trades_total{result=open|win|loss}

bot_equity_usd

bot_model_mode{mode}

bot_vol_risk_factor

bot_walk_forward_fits_total

Core files & roles

env.go — loads whitelisted env keys into config (no arbitrary import).

config.go — Config with all trading knobs (incl. extended toggles).

metrics.go — Prometheus exposition and gauges/counters.

trader.go — state, synchronized step() loop, pyramiding logic, fees/PnL, runner trailing, persistence to STATE_FILE.

broker.go / broker_bridge.go — broker interface and Bridge-backed implementation (PlaceMarketQuote(...)), and PaperBroker.

live.go — live loop / sync with bridge prices (tick nudging).

backtest.go — CSV backtest (1m candles), train/test split, warmup, 50–60%+ features.

strategy.go / model.go / indicators.go — thresholds and model heads (logistic baseline/extended), SMA/RSI/ZScore.

Trading config (env keys; exact names)

# Trading target / cadence
PRODUCT_ID=BTC-USD
GRANULARITY=ONE_MINUTE
USE_TICK_PRICE=true
TICK_INTERVAL_SEC=1
CANDLE_RESYNC_SEC=60

# Risk & sizing
ORDER_MIN_USD=5.00
MAX_DAILY_LOSS_PCT=2.0
RISK_PER_TRADE_PCT=20.0
USD_EQUITY=68.5
MAX_HISTORY_CANDLES=5000

# Position controls
DRY_RUN=false
LONG_ONLY=true
ALLOW_PYRAMIDING=true
PYRAMID_MIN_ADVERSE_PCT=1.5
PYRAMID_DECAY_LAMBDA=0.02
PYRAMID_MIN_SECONDS_BETWEEN=0
MAX_CONCURRENT_LOTS=20
PYRAMID_DECAY_MIN_PCT=0.4

# Optional per-add TP decay for scalp lots
SCALP_TP_DECAY_ENABLE=true
SCALP_TP_DEC_MODE=exp
SCALP_TP_DEC_PCT=0.20
SCALP_TP_DECAY_FACTOR=0.9802
SCALP_TP_MIN_PCT=1.55

# Exits (TP/SL + runner trailing)
TAKE_PROFIT_PCT=1.9
STOP_LOSS_PCT=1000.00
TRAIL_ACTIVATE_PCT=1.9
TRAIL_DISTANCE_PCT=0.4

# Fees & state
FEE_RATE_PCT=0.75
STATE_FILE=/opt/coinbase/state/bot_state.json

# Strategy thresholds
BUY_THRESHOLD=0.45
SELL_THRESHOLD=0.55
USE_MA_FILTER=true
BACKTEST_SLEEP_MS=100

# Ops
PORT=8080
BRIDGE_URL=http://bridge:8787
USE_LIVE_EQUITY=true
MODEL_MODE=extended
WALK_FORWARD_MIN=1
VOL_RISK_ADJUST=true
DAILY_BREAKER_MARK_TO_MARKET=true
# SLACK_WEBHOOK=https://hooks.slack.com/services/XXX/YYY/ZZZ


Behavioral highlights

Synchronized step() uses latest candle/tick; if candle time is zero uses time.Now().UTC() for daily accounting.

Long-only guard: discretionary SELL entries ignored while lots open.

Sizing: quote = max(ORDER_MIN_USD, RISK_PER_TRADE_PCT% * equityUSD); base=quote/price.

Pyramiding (env-gated):

Spacing: PYRAMID_MIN_SECONDS_BETWEEN.

Adverse move vs last entry with time-decay: eff = base * exp(-PYRAMID_DECAY_LAMBDA * minutes_since_lastAdd), floored at PYRAMID_DECAY_MIN_PCT.

Adds capped by MAX_CONCURRENT_LOTS.

First lot is runner; others are scalps with optional per-add TP decay (linear/exp) floored at SCALP_TP_MIN_PCT.

Runner trailing: activates at TRAIL_ACTIVATE_PCT, trails by TRAIL_DISTANCE_PCT; promotes newest remaining lot as runner when runner closes.

Fees/PnL: uses broker Price, BaseSize, CommissionUSD when present; subtracts entry + exit fees; warns on partial fills.

Daily breaker: MAX_DAILY_LOSS_PCT enforced.

Persistence: equity/PnL/lots saved to STATE_FILE atomically (.tmp then rename).

New live-order feature (insufficient funds fallback)

In trader.go open-order path, if PlaceMarketQuote(...) fails with an “insufficient funds” error (case-insensitive match on insufficient/fund/HTTP 400/422 style messages), the bot logs:

[WARN] open order %.2f USD failed (%v); retrying with ORDER_MIN_USD=%.2f


…and retries once using quote = ORDER_MIN_USD. On second failure it returns the error without further retries.
(No changes to public structs, env keys, metrics, or log formats outside the new WARN line.)

4) Bridge (FastAPI, HTTP :8787)

Image: ghcr.io/<owner>/coinbase-bridge:latest
Entrypoint: uvicorn app:app --host 0.0.0.0 --port 8787
HTTP:

GET /health

GET /accounts?limit=

GET /product/{product_id}

GET /candles?granularity&limit&product_id (limit ≤ 350)

GET /price?product_id= (uses latest WS tick or marks stale)

POST /orders/market_buy

POST /order/market (BUY/SELL by quote size)

WebSocket (optional): subscribes to Advanced Trade WS and caches _last_ticks per product.

Bridge env (mounted from /opt/coinbase/env/bridge.env)

# Coinbase API
COINBASE_API_KEY_NAME=organizations/.../apiKeys/...
COINBASE_API_PRIVATE_KEY=-----BEGIN EC PRIVATE KEY-----\n...\n-----END EC PRIVATE KEY-----\n
COINBASE_API_BASE=https://api.coinbase.com

# Bridge runtime
PORT=8787

# Optional WS
COINBASE_WS_ENABLE=true
COINBASE_WS_PRODUCTS=BTC-USD[,ETH-USD,...]
COINBASE_WS_URL=wss://advanced-trade-ws.coinbase.com
COINBASE_WS_STALE_SEC=10

5) Monitoring stack (Docker Compose in ~/coinbase/monitoring/docker-compose.yml)

Network: monitoring_network (bridge driver).
Volumes: monitoring_prometheus_data, monitoring_grafana_data.

Services & images

bot
Image: ghcr.io/<owner>/coinbase-bot:latest
Command: ["-live","-interval","1"] (image entrypoint is /app/bot)
Volumes:

/opt/coinbase/env:/opt/coinbase/env:ro

/opt/coinbase/state:/opt/coinbase/state
Env file: /opt/coinbase/env/bot.env
Expose: 8080
Logging: json-file (max-size=10m, max-file=5)
Healthcheck (when enabled): wget -qO- http://localhost:8080/healthz
Network alias: bot, coinbase-bot

bridge
Image: ghcr.io/<owner>/coinbase-bridge:latest
Env file: /opt/coinbase/env/bridge.env
Expose: 8787
Healthcheck (when enabled): wget -qO- http://localhost:8787/health
Network alias: bridge

prometheus
Image: prom/prometheus:latest
Ports: 9090:9090
Volumes:

./prometheus/prometheus.yml:/etc/prometheus/prometheus.yml

monitoring_prometheus_data:/prometheus
Flags (present in compose): --storage.tsdb.retention.size=2GB

alertmanager
Image: prom/alertmanager:latest
Ports: 9093:9093
Volumes: ./alertmanager/alertmanager.yml:/etc/alertmanager/alertmanager.yml:ro

grafana
Image: grafana/grafana:latest
Ports: 3000:3000
Env: GF_SECURITY_ADMIN_USER=admin, GF_SECURITY_ADMIN_PASSWORD=admin
Volume: monitoring_grafana_data:/var/lib/grafana
Depends on: prometheus, bot, bridge

Prometheus config (monitoring/prometheus/prometheus.yml)

global:
  scrape_interval: 15s
rule_files:
  - /etc/prometheus/rules.yml
alerting:
  alertmanagers:
    - static_configs:
        - targets: ["alertmanager:9093"]
scrape_configs:
  - job_name: "prometheus"
    static_configs:
      - targets: ["localhost:9090"]
  - job_name: "coinbase-bot"
    static_configs:
      - targets: ["bot:8080"]

6) Container images (GHCR)

Bot image: ghcr.io/${{ github.repository_owner }}/coinbase-bot:latest and :${{ github.sha }}

Bridge image: ghcr.io/${{ github.repository_owner }}/coinbase-bridge:latest and :${{ github.sha }}

Root Dockerfile (bot)

Builds Go app and produces /app/bot (static), then runs with a distroless base.

Entrypoint: /app/bot (so compose uses ["-live","-interval","1"] as args).

bridge/Dockerfile

Builds a minimal Python image with FastAPI + Uvicorn; CMD runs uvicorn app:app --host 0.0.0.0 --port 8787.

7) CI/CD (GitHub Actions)

.github/workflows/ci.yml — “Go vet & test”

Trigger: push to main.

Steps: checkout → setup Go → cache modules → go vet ./... → go test -count=1 ./....

.github/workflows/docker.yml — “Build & Push Images”

Trigger: push to main.

Login to GHCR with built-in ${{ secrets.GITHUB_TOKEN }}.

Build & push:

BOT_IMAGE=ghcr.io/${{ github.repository_owner }}/coinbase-bot:{latest,${{ github.sha }}}

BRIDGE_IMAGE=ghcr.io/${{ github.repository_owner }}/coinbase-bridge:{latest,${{ github.sha }}}

Platform: linux/amd64.

.github/workflows/deploy.yml — “Deploy to Linode”

Trigger: workflow_run after “Build & Push Images” completes (only on main, only on success).

Uses SSH to run remote rollout on the VM.

Repo Secrets required:

SSH_PRIVATE_KEY (PEM/OPENSSH private key matching a public key in /home/chidi/.ssh/authorized_keys on the VM)

SSH_HOST (e.g., 172.236.14.121)

SSH_USER (e.g., chidi)

Repo Variable:

DEPLOY_DIR (e.g., /home/chidi/coinbase/monitoring)

Remote script (exact behavior):

Ensures git clone exists at /home/chidi/coinbase; sets remote to HTTPS.

git fetch --all && git reset --hard origin/main to sync code.

cd "$DEPLOY_DIR" and docker compose up -d --pull=always --force-recreate.

docker image prune -f.

8) Dev & Ops runbook (VM)

Validate compose:

docker compose -f /home/chidi/coinbase/monitoring/docker-compose.yml config >/dev/null && echo "compose OK"


Bring up stack:

cd /home/chidi/coinbase/monitoring
docker compose up -d --pull=always --force-recreate


Health checks (in docker network):

docker run --rm --network=monitoring_monitoring_network curlimages/curl:8.8.0 \
  curl -fsS http://bot:8080/healthz && echo "bot OK"
docker run --rm --network=monitoring_monitoring_network curlimages/curl:8.8.0 \
  curl -fsS http://bridge:8787/health && echo "bridge OK"


Prometheus flags introspection:

docker inspect monitoring-prometheus-1 --format '{{json .Config.Cmd}}' | jq -r .

Please provide the following for cross-check (so I can finalize Text B into your invariant baseline)

To ensure every path/flag in Text B exactly matches what’s in your repo and on the VM, please paste or upload:

monitoring/docker-compose.yml (current committed version)

monitoring/prometheus/prometheus.yml

Root Dockerfile (bot) and bridge/Dockerfile

.github/workflows/ci.yml, .github/workflows/docker.yml, .github/workflows/deploy.yml (as currently committed)

trader.go (current committed) — I’ll confirm the insufficient-funds retry WARN line is present and in the open-order path

The two env files you actually mount (you can redact secrets):

/opt/coinbase/env/bot.env

/opt/coinbase/env/bridge.env

On the VM, the output of:

cd /home/chidi/coinbase/monitoring
docker compose images | grep ghcr.io
docker compose ps


Once I verify these against Text B, I’ll mark it final and you can lock it into your invariant baseline.]

Task:
Please integrate B directly inside A where it naturally belongs (not as a separate list).

Keep all of A’s original lines intact unless they must be expanded to fit B.

Do not delete or paraphrase A — if you must replace a line, show the removed text separately at the end under a “Dropped / Replaced Lines” section.

The result must read as a single, seamless, stable specification.

Expected Outcome:

The merged spec (A + B, with inline integration and dropped/replaced lines noted) is the current truth of the project at the end of this Phase

Merged Spec = A + B woven together into one cohesive, stable specification.

Rules:

The merged document is now the authoritative single source:

It preserves every invariant line from A unless it was explicitly superseded.

It embeds all of B’s details (new files, env keys, ports, commands, metrics, runtime behaviors).

The “Dropped / Replaced Lines” section is only for traceability — so you know exactly what changed from the original baseline.
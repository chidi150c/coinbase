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
│       └── prometheus.yml
└── verify.txt


Integrated details (current truth):

The repository also contains:

Root Dockerfile (builds the bot binary at /app/bot using a distroless final stage).

.dockerignore

Makefile

.github/workflows/ci.yml, .github/workflows/docker.yml, .github/workflows/deploy.yml

We sometimes create a local, not committed monitoring/docker-compose.override.yml on the VM only (e.g., to temporarily disable healthchecks). This file is not part of the baseline.

2) Runtime directories on the VM (outside the repo)

/opt/coinbase/env/ — holds bot.env and bridge.env (mounted read-only).

/opt/coinbase/state/ — holds persisted bot state.

Persisted state file (env-configured):
STATE_FILE=/opt/coinbase/state/bot_state.json

Integrated details (from Text B):
/opt/coinbase/state/bot_state.json — persisted bot state file (path set by STATE_FILE) used by trader.go.

3) Services & Networking (~/coinbase/monitoring/docker-compose.yml)

bot

Image: ghcr.io/<owner>/coinbase-bot:latest

Command: ["-live","-interval","1"] (image entrypoint is /app/bot)

Volumes:

/opt/coinbase/env:/opt/coinbase/env:ro

/opt/coinbase/state:/opt/coinbase/state

Env file: /opt/coinbase/env/bot.env

Expose: 8080

Restart: unless-stopped

Healthcheck (when enabled): wget -qO- http://localhost:8080/healthz >/dev/null 2>&1 || exit 1

Logging: json-file (max-size=10m, max-file=5)

Networks: monitoring_network with aliases [bot, coinbase-bot]

bridge

Image: ghcr.io/<owner>/coinbase-bridge:latest

Env file: /opt/coinbase/env/bridge.env

Expose: 8787

Restart: unless-stopped

Healthcheck (when enabled): wget -qO- http://localhost:8787/health >/dev/null 2>&1 || exit 1

Networks: monitoring_network with alias bridge

prometheus

Image: prom/prometheus:latest

Ports: 9090:9090

Volumes:

./prometheus/prometheus.yml:/etc/prometheus/prometheus.yml

monitoring_prometheus_data:/prometheus

Restart: unless-stopped

Flag present: --storage.tsdb.retention.size=2GB

alertmanager

Image: prom/alertmanager:latest

Ports: 9093:9093

Volumes: ./alertmanager/alertmanager.yml:/etc/alertmanager/alertmanager.yml:ro

Restart: unless-stopped

grafana

Image: grafana/grafana:latest

Ports: 3000:3000

Environment:

GF_SECURITY_ADMIN_USER=admin

GF_SECURITY_ADMIN_PASSWORD=admin

Volumes: monitoring_grafana_data:/var/lib/grafana

Depends on: prometheus, bot, bridge

Networks

Single: monitoring_network (bridge driver)

Volumes

monitoring_prometheus_data

monitoring_grafana_data

Prometheus config (monitoring/prometheus/prometheus.yml):

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


Integrated details (from Text A):
Ports remain: bot :8080, bridge :8787. Prometheus scrapes bot at http://bot:8080/metrics.

4) Go Bot (core)
Files & roles

env.go — loads whitelisted env keys into config.

config.go — Config with all trading knobs; extended toggles; fee config.

metrics.go — Prometheus exposition and counters/gauges on :8080/metrics.

trader.go — in-memory state + synchronized step() loop; pyramiding; runner trailing; fees & PnL; persistence to STATE_FILE; daily breaker; partial-fill & commission handling.

broker.go / broker_bridge.go — broker interface and Bridge-backed implementation (PlaceMarketQuote(...)); PaperBroker supported.

live.go — live loop with tick nudging via bridge.

backtest.go — CSV backtest (1m candles), train/test split, warmup pacing, model fit.

strategy.go / model.go / indicators.go — BUY/SELL thresholds and model heads (baseline/extended); SMA/RSI/ZScore.

HTTP surfaces (bot)

:8080/healthz

:8080/metrics (Prometheus)

Metrics (names)

bot_orders_total{mode,side}

bot_decisions_total{signal}

bot_trades_total{result=open|win|loss}

bot_equity_usd

bot_model_mode{mode}

bot_vol_risk_factor

bot_walk_forward_fits_total

Trading config (env keys; exact names)
# === Trading target / cadence ===
PRODUCT_ID=BTC-USD
GRANULARITY=ONE_MINUTE
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

# --- optional: per-add TP decay for scalp lots ---
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

Behavior (invariants)

Synchronized step() uses latest candle/tick; if candle time is zero, uses time.Now().UTC() for daily accounting.

Long-only guard: discretionary SELL entries ignored while lots are open; exits by TP/SL/runner trailing.

Sizing: quote = max(ORDER_MIN_USD, (RISK_PER_TRADE_PCT/100)*equityUSD); base = quote/price.

Pyramiding: spacing via PYRAMID_MIN_SECONDS_BETWEEN; adverse move vs last entry with time-decay eff = basePct * exp(-PYRAMID_DECAY_LAMBDA * minutes_since_lastAdd) floored at PYRAMID_DECAY_MIN_PCT; cap MAX_CONCURRENT_LOTS.

Runner: first lot becomes runner (TP stretched ×2, SL baseline); other lots are scalps with optional per-add TP decay (linear or exponential) floored at SCALP_TP_MIN_PCT.

Trailing (runner): activates at TRAIL_ACTIVATE_PCT, trails by TRAIL_DISTANCE_PCT. On runner close, newest remaining lot is promoted and its trailing fields reset.

Fees & PnL: prefers broker-provided Price, BaseSize, CommissionUSD; warns on partial fills; subtracts entry + exit fees.

Daily breaker: MAX_DAILY_LOSS_PCT enforced.

Persistence: atomic save to STATE_FILE (.tmp then rename).

New live-order feature (insufficient funds fallback)

In trader.go open-order path, if PlaceMarketQuote(...) fails with “insufficient funds” (case-insensitive match on insufficient/fund or relevant status), the bot logs:

[WARN] open order %.2f USD failed (%v); retrying with ORDER_MIN_USD=%.2f


…then retries once using quote = ORDER_MIN_USD. On second failure it returns the error.

5) Python Bridge (FastAPI) — bridge/app.py

Runtime: uvicorn app:app --host 0.0.0.0 --port 8787
Endpoints:

GET /health

GET /accounts?limit=

GET /product/{product_id}

GET /candles?granularity&limit&product_id (limit ≤ 350)

GET /price?product_id= (uses latest WS tick or marks stale)

POST /orders/market_buy

POST /order/market (BUY/SELL by quote size)

WebSocket (optional): subscribes to Advanced Trade WS; caches _last_ticks by product.

Bridge env (/opt/coinbase/env/bridge.env):

COINBASE_API_KEY_NAME=organizations/.../apiKeys/...
COINBASE_API_PRIVATE_KEY=-----BEGIN EC PRIVATE KEY-----\n...\n-----END EC PRIVATE KEY-----\n
COINBASE_API_BASE=https://api.coinbase.com
PORT=8787
# Optional WS
COINBASE_WS_ENABLE=true
COINBASE_WS_PRODUCTS=BTC-USD[,ETH-USD,...]
COINBASE_WS_URL=wss://advanced-trade-ws.coinbase.com
COINBASE_WS_STALE_SEC=10

6) Metrics & Monitoring

Prometheus scrapes bot at http://bot:8080/metrics.

Grafana (admin/admin) visualizes equity curve, trades, MA overlays, breaker status, PnL/daily changes, volatility factor.

Alertmanager integrates with Slack; alerts on downtime, “0-decisions” windows, equity drops.

7) Container Images (GHCR)

Bot: ghcr.io/${{ github.repository_owner }}/coinbase-bot:latest and :${{ github.sha }}

Bridge: ghcr.io/${{ github.repository_owner }}/coinbase-bridge:latest and :${{ github.sha }}

Root Dockerfile (bot)

Builds statically-linked Go binary at /app/bot; distroless final image; entrypoint /app/bot.

bridge/Dockerfile

FastAPI + Uvicorn; CMD ["uvicorn", "app:app", "--host", "0.0.0.0", "--port", "8787"].

8) CI/CD (GitHub Actions)

.github/workflows/ci.yml — “Go vet & test”

on: push to main

Steps: checkout → setup Go → cache modules → go vet ./... → go test -count=1 ./...

.github/workflows/docker.yml — “Build & Push Images”

on: push to main

Login to GHCR with built-in ${{ secrets.GITHUB_TOKEN }}

Build & push both images (bot & bridge) to ghcr.io with tags :latest and :${{ github.sha }} (platform linux/amd64)

.github/workflows/deploy.yml — “Deploy to Linode”

on: workflow_run of “Build & Push Images” (only on main, only if success)

Secrets required:

SSH_PRIVATE_KEY (matches a public key in /home/chidi/.ssh/authorized_keys on the VM)

SSH_HOST (e.g., 172.236.14.121)

SSH_USER (e.g., chidi)

Variables:

DEPLOY_DIR (e.g., /home/chidi/coinbase/monitoring)

Remote rollout script (exact):

Ensure clone at /home/chidi/coinbase (remote set to HTTPS); if absent, git clone.

git fetch --all && git reset --hard origin/main to sync code.

cd "$DEPLOY_DIR" and docker compose up -d --pull=always --force-recreate.

docker image prune -f.

9) Dev & Ops Runbook (VM)

Validate compose

docker compose -f /home/chidi/coinbase/monitoring/docker-compose.yml config >/dev/null && echo "compose OK"


Bring up stack

cd /home/chidi/coinbase/monitoring
docker compose up -d --pull=always --force-recreate


In-network health checks

docker run --rm --network=monitoring_monitoring_network curlimages/curl:8.8.0 \
  curl -fsS http://bot:8080/healthz && echo "bot OK"

docker run --rm --network=monitoring_monitoring_network curlimages/curl:8.8.0 \
  curl -fsS http://bridge:8787/health && echo "bridge OK"


Prometheus flags (confirm retention)

docker inspect monitoring-prometheus-1 --format '{{json .Config.Cmd}}' | jq -r .

10) Backtest (backtest.go)

Loads data/BTC-USD.csv (1m candles; ~5000–6000+ supported).

Train/test split (70/30 default; configurable), warmup 50.

Pacing via BACKTEST_SLEEP_MS.

Uses same metric updates as live mode; DRY_RUN disables live order placement.

Runtime configuration uses MAX_HISTORY_CANDLES for historical window sizing.

11) State, Logs, Safety

STATE_FILE=/opt/coinbase/state/bot_state.json must be writable via the bind mount.

Logs include [TICK] and [DEBUG] lines; circuit breaker via MAX_DAILY_LOSS_PCT.

Spot-only LONG mode: LONG_ONLY=true.

ORDER_MIN_USD enforced as absolute floor on quote sizing.
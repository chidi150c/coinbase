You are joining an existing Coinbase Advanced Trade bot project.
Invariant baseline (must remain stable and NOT be re-generated):

Codebase

Go bot package (~/coinbase/):
env.go, config.go, indicators.go, model.go, strategy.go, trader.go, metrics.go, backtest.go, live.go, main.go, broker.go, broker_bridge.go, broker_paper.go

Datasets/Bridge/Tools:

~/coinbase/data/BTC-USD.csv

~/coinbase/data/app.py (FastAPI bridge)

~/coinbase/tools/backfill_bridge.go

~/coinbase/tools/backfill_bridge_paged.go

Monitoring stack (~/coinbase/monitoring/):

docker-compose.yml

prometheus/prometheus.yml

Flags / Modes

-live

-backtest <csv>

-interval <seconds>

Endpoints

Bot (8080): /healthz, /metrics

Bridge (8787): /health, /accounts?limit=, /product/{id}, /candles?product_id=&granularity=&limit=&start=&end=, /orders/market_buy, /order/market

Prometheus: :9090

Grafana: :3000

Metrics

bot_orders_total{mode="paper|live",side="BUY|SELL"}

bot_decisions_total{signal="buy|sell|flat"}

bot_equity_usd

Environment
Bot (/opt/coinbase/env/bot.env)
DRY_RUN=true
LONG_ONLY=true
ORDER_MIN_USD=5.00
MAX_DAILY_LOSS_PCT=1.0
RISK_PER_TRADE_PCT=0.25
USD_EQUITY=1000.00
TAKE_PROFIT_PCT=0.8
STOP_LOSS_PCT=0.4
PORT=8080
BRIDGE_URL=http://bridge:8787
PRODUCT_ID=BTC-USD
GRANULARITY=ONE_MINUTE
BUY_THRESHOLD=0.50
SELL_THRESHOLD=0.50
USE_MA_FILTER=false
BACKTEST_SLEEP_MS=500

Bridge (/opt/coinbase/env/bridge.env)
COINBASE_API_KEY=...
COINBASE_API_SECRET=...
PORT=8787


Permissions: /opt/coinbase/env/ → drwxr-x--- root:docker ; files → -rw-r----- root:docker

Strategy / Model

Logistic-like pUp with MA(10)/MA(30) filter

Tunable thresholds via env (BUY_THRESHOLD, SELL_THRESHOLD, USE_MA_FILTER, MA_FAST, MA_SLOW)

Monitoring Stack
Services (docker-compose)

bot (golang:1.23)

Volumes: ..:/app:ro, /opt/coinbase/env/bot.env:/app/.env:ro

Command: /usr/local/go/bin/go run . -live -interval 15

Expose: 8080

Network: monitoring_monitoring_network

bridge (python:3.11-slim)

Volumes: ../bridge:/app/bridge:ro, /opt/coinbase/env/bridge.env:/app/.env:ro

Command: pip install fastapi==0.115.0 uvicorn==0.30.6 coinbase-advanced-py==1.0.0 python-dotenv==1.0.1 && uvicorn app:app --host 0.0.0.0 --port 8787

Expose: 8787

prometheus (prom/prometheus:latest)

Ports: 9090:9090

Volumes: ./prometheus/prometheus.yml:/etc/prometheus/prometheus.yml, monitoring_prometheus_data:/prometheus

grafana (grafana/grafana:latest)

Ports: 3000:3000

Env: GF_SECURITY_ADMIN_USER=admin, GF_SECURITY_ADMIN_PASSWORD=admin

Volumes: monitoring_grafana_data:/var/lib/grafana

Prometheus config
global:
  scrape_interval: 15s
scrape_configs:
  - job_name: "coinbase-bot"
    static_configs:
      - targets: ["bot:8080"]
  - job_name: "prometheus"
    static_configs:
      - targets: ["localhost:9090"]

Volumes

monitoring_prometheus_data

monitoring_grafana_data

Backfill Tools

backfill_bridge.go: one-shot 300 candles → CSV

backfill_bridge_paged.go: multi-page (~6000 rows) → CSV

Runtime / Logs

Bot live: /usr/local/go/bin/go run . -live -interval 15

Bot backtest: /usr/local/go/bin/go run . -backtest /app/data/BTC-USD.csv -interval 1

Logs:

serving metrics on :8080/metrics

[BT] i=N msg=... (FLAT, HOLD, PAPER BUY/SELL)

Backtest complete. Wins=X Losses=Y Equity=Z

Error if bridge down: candles err: connect: connection refused

Bridge: uvicorn app:app --host 0.0.0.0 --port 8787

Prometheus: scrapes bot:8080 and localhost:9090

Grafana: http://localhost:3000 (admin/admin)

Current Dataset

Path: ~/coinbase/data/BTC-USD.csv

Rows: ~6001 (header + 6000 candles)

Granularity: ONE_MINUTE

Span: ~100 hours (~4.2 days)

Operating rules:

Do NOT rewrite, reformat, or re-emit existing files unless explicitly instructed to replace a specific file.

Default to INCREMENTAL CHANGES ONLY. If you need context from an existing file, ASK ME to paste that file or the relevant snippet.

Never change defaults to place real orders. Keep DRY_RUN=true & LONG_ONLY=true unless I explicitly opt in.

Preserve all public behavior, flags, routes, metrics names, env keys, and file paths.

How to respond:

First, list Required Inputs you need (e.g., “paste current strategy.go”, “what Slack webhook URL?”, “what Docker base image?”).

Then provide a brief Plan (1–5 bullets).

Deliver changes as:
a) Minimal unified diffs/patches for specific files (or full new file content if it’s a new file), and/or
b) Exact shell commands and env additions (clearly marked), and
c) A short Runbook to test/verify (/healthz, /metrics, backtest, live dry-run).

Constraints:

Keep dependencies minimal; if adding any, list precise version pins and why.

Maintain metrics compatibility and logging style.

Any live-trading change must include an explicit safety callout and a revert/kill instruction.

Goal: Extend the bot safely and incrementally [...] without repeating or replacing the existing foundation.
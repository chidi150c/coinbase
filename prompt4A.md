You are joining an existing Coinbase Advanced Trade bot project.
Invariant baseline (must remain stable and NOT be re-generated):

Codebase

Go bot package (~/coinbase/):
env.go, config.go, indicators.go, model.go, strategy.go, trader.go, metrics.go, backtest.go, live.go, main.go, broker.go, broker_bridge.go, broker_paper.go
Extra: ~/coinbase/data/BTC-USD.csv

Python FastAPI bridge (~/coinbase/bridge/app.py)
Wraps coinbase.rest.RESTClient

Monitoring stack (~/coinbase/monitoring/):
docker-compose.yml, prometheus/prometheus.yml

Flags / Modes

-live

-backtest <csv>

-interval <seconds>

HTTP Endpoints

Bot: /healthz, /metrics (port 8080)

Bridge: /health, /accounts?limit=, /product/{id}, /candles?product_id=&granularity=&limit=, /orders/market_buy, /order/market (port 8787)

Prometheus: :9090

Grafana: :3000

Metrics (Prometheus)

bot_orders_total{mode="paper|live",side="BUY|SELL"}

bot_decisions_total{signal="buy|sell|flat"}

bot_equity_usd

Environment
bot.env (/opt/coinbase/env/bot.env)
DRY_RUN=true
LONG_ONLY=true
ORDER_MIN_USD=5.00
MAX_DAILY_LOSS_PCT=1.0
BUY_THRESHOLD=0.55
SELL_THRESHOLD=0.45
USE_MA_FILTER=true
MA_FAST=10
MA_SLOW=30
BRIDGE_URL=http://coinbase-bridge:8787
PORT=8080

bridge.env (/opt/coinbase/env/bridge.env)
COINBASE_API_KEY=…
COINBASE_API_SECRET=…
PORT=8787


Permissions: /opt/coinbase/env/ → drwxr-x--- root:docker; files → -rw-r----- root:docker

Strategy / Model

Logistic-like pUp with MA(10)/MA(30) filter

Thresholds tunable via env (BUY_THRESHOLD, SELL_THRESHOLD, USE_MA_FILTER, MA_FAST, MA_SLOW)

Monitoring Stack
docker-compose.yml services

bot (coinbase-bot)

Image: golang:1.23

Volumes: ..:/app:ro, /opt/coinbase/env/bot.env:/app/.env:ro

Command: /usr/local/go/bin/go run . -live -interval 15

Expose: 8080

Network: monitoring_network

bridge (coinbase-bridge)

Image: python:3.11-slim

Volumes: ../bridge:/app/bridge:ro, /opt/coinbase/env/bridge.env:/app/.env:ro

Command: bash -lc "pip install --no-cache-dir fastapi==0.115.0 uvicorn==0.30.6 coinbase-advanced-py==1.0.0 && uvicorn app:app --host 0.0.0.0 --port 8787"

Expose: 8787

Network: monitoring_network

prometheus

Image: prom/prometheus:latest

Ports: 9090:9090

Volumes: ./prometheus/prometheus.yml:/etc/prometheus/prometheus.yml, monitoring_prometheus_data:/prometheus

Network: monitoring_network

grafana

Image: grafana/grafana:latest

Ports: 3000:3000

Env: GF_SECURITY_ADMIN_USER=admin, GF_SECURITY_ADMIN_PASSWORD=admin

Volumes: monitoring_grafana_data:/var/lib/grafana

Network: monitoring_network

prometheus.yml
global:
  scrape_interval: 15s
scrape_configs:
  - job_name: "coinbase-bot"
    static_configs:
      - targets: ["coinbase-bot:8080"]
  - job_name: "prometheus"
    static_configs:
      - targets: ["localhost:9090"]

Volumes

monitoring_prometheus_data

monitoring_grafana_data

Runtime / Logs

Bot:

Live: /usr/local/go/bin/go run . -live -interval 15

Backtest: /usr/local/go/bin/go run . -backtest /app/data/BTC-USD.csv -interval 1

Logs: serving metrics on :8080/metrics, Starting coinbase-bridge — product=BTC-USD dry_run=true

Error if bridge down: candles err: connect: connection refused

Bridge:

Command: uvicorn app:app --host 0.0.0.0 --port 8787

Prometheus:

Config path: ~/coinbase/monitoring/prometheus/prometheus.yml

Scrapes localhost:9090, coinbase-bot:8080

Grafana:

Port: 3000

Admin creds: admin/admin

Datasource: http://prometheus:9090

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
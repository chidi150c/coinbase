You are joining an existing Coinbase Advanced Trade bot project. Invariant baseline (must remain stable and NOT be re-generated):

Codebase (~/coinbase/)

Go sources: env.go, config.go, indicators.go, model.go, strategy.go, trader.go, metrics.go, backtest.go, live.go, main.go, broker.go, broker_bridge.go, broker_paper.go

Data: data/BTC-USD.csv (~6001 rows, ONE_MINUTE, ~100h span)

Bridge: bridge/app.py, bridge/requirements.txt

Tools: tools/backfill_bridge.go, tools/backfill_bridge_paged.go

Misc: .env, .env.example, .git*, README.md, verify.txt, bot.log, bot.pid

Monitoring (~/coinbase/monitoring/)

Files: docker-compose.yml, prometheus/prometheus.yml, grafana/, grafana-data/

Volumes: monitoring_prometheus_data, monitoring_grafana_data

Network: monitoring_network

Services (docker-compose.yml):

bot (golang:1.23): working_dir=/app, volumes=..:/app, env_file=/opt/coinbase/env/bot.env

Backtest: /usr/local/go/bin/go run . -backtest /app/data/BTC-USD.csv -interval 1, restart="no"

Live: /usr/local/go/bin/go run . -live -interval 15, restart=unless-stopped

expose: 8080; aliases: bot, coinbase-bot

bridge (python:3.11-slim): working_dir=/app/bridge, volumes=../bridge:/app/bridge:ro, env_file=/opt/coinbase/env/bridge.env

command: pip install fastapi uvicorn coinbase-advanced-py python-dotenv && uvicorn app:app --host 0.0.0.0 --port 8787

expose: 8787; restart=unless-stopped; alias: bridge

prometheus (prom/prometheus:latest): ports 9090:9090; volumes: ./prometheus/prometheus.yml:/etc/prometheus/prometheus.yml, monitoring_prometheus_data:/prometheus

grafana (grafana/grafana:latest): ports 3000:3000; env: GF_SECURITY_ADMIN_USER=admin, GF_SECURITY_ADMIN_PASSWORD=admin; volumes: monitoring_grafana_data:/var/lib/grafana; depends_on: [prometheus, bot, bridge]

Prometheus (~/coinbase/monitoring/prometheus/prometheus.yml)

global: scrape_interval=15s

scrape_configs:

job_name="prometheus", targets=["localhost:9090"]

job_name="coinbase-bot", targets=["bot:8080"]

Environment (/opt/coinbase/env/)

bot.env:
PRODUCT_ID=BTC-USD
GRANULARITY=ONE_MINUTE
DRY_RUN=false
LONG_ONLY=true
ORDER_MIN_USD=5.00
MAX_DAILY_LOSS_PCT=1.0
RISK_PER_TRADE_PCT=0.25
USD_EQUITY=1000.00
TAKE_PROFIT_PCT=0.8
STOP_LOSS_PCT=0.4
BUY_THRESHOLD=0.48
SELL_THRESHOLD=0.50
USE_MA_FILTER=false
BACKTEST_SLEEP_MS=500
PORT=8080
BRIDGE_URL=http://bridge:8787

bridge.env:
COINBASE_API_KEY_NAME=organizations/.../apiKeys/...
COINBASE_API_PRIVATE_KEY=-----BEGIN EC PRIVATE KEY----- ...
COINBASE_API_BASE=https://api.coinbase.com

PORT=8787

Defaults (from code): BUY_THRESHOLD=0.55, SELL_THRESHOLD=0.45, USE_MA_FILTER=true, DRY_RUN=true, LONG_ONLY=true, ORDER_MIN_USD=5, MAX_DAILY_LOSS_PCT=1.0, RISK_PER_TRADE_PCT=0.25, USD_EQUITY=1000, TAKE_PROFIT_PCT=0.8, STOP_LOSS_PCT=0.4, BACKTEST_SLEEP_MS=0, PORT=8080, BRIDGE_URL=http://127.0.0.1:8787

HTTP Endpoints

Bot (8080): GET /healthz, GET /metrics

Bridge (8787): GET /health, GET /accounts?limit=, GET /product/{id}, GET /candles?product_id=&granularity=&limit=&start=&end=, POST /order/market {product_id, side=BUY|SELL, quote_size, client_order_id?}, POST /orders/market_buy

Metrics (Prometheus Exported)

bot_orders_total{mode="paper|live",side="BUY|SELL"}

bot_decisions_total{signal="buy|sell|flat"}

bot_equity_usd

Features & Behavior

Strategy: pUp scoring, BUY_THRESHOLD / SELL_THRESHOLD, optional MA(10)/MA(30) filter

Trader: long-only, equity tracking, risk sizing, stop/take, circuit breaker

Safety: DRY_RUN, ORDER_MIN_USD=5, RISK_PER_TRADE_PCT=0.25, TAKE_PROFIT_PCT=0.8, STOP_LOSS_PCT=0.4, MAX_DAILY_LOSS_PCT=1.0

Backtest: 70/30 split, warmup 50, BACKTEST_SLEEP_MS pacing, updates bot_equity_usd

Live: polls /candles every -interval (default 60, typical 15), runs trader

Startup log:
Starting coinbase-bridge — product=BTC-USD dry_run=<true|false>
[SAFETY] LONG_ONLY=<..> | ORDER_MIN_USD=<..> | RISK_PER_TRADE_PCT=<..> | MAX_DAILY_LOSS_PCT=<..> | TAKE_PROFIT_PCT=<..> | STOP_LOSS_PCT=<..>

Grafana Dashboard (~/coinbase/monitoring/grafana/bot-dashboard.json)

Panels:

Equity (USD): bot_equity_usd

Decisions: increase(bot_decisions_total{signal}[5m])

Orders Placed: increase(bot_orders_total{mode="live",side}[1h])

Circuit Breaker Status:
expr: clamp_max(sum(increase(bot_decisions_total{signal="flat"}[5m])),1) - clamp_max(sum(increase(bot_orders_total{mode="live"}[5m])),1)
mapping: 0=OK, 1=ACTIVE

Total Trades: sum(bot_orders_total{mode="live"})

Daily P/L: bot_equity_usd - (bot_equity_usd offset 1d)

Tools (~/coinbase/tools/)

backfill_bridge.go:
docker compose run --rm bot go run ./tools/backfill_bridge.go -product BTC-USD -granularity ONE_MINUTE -limit 300 -out data/BTC-USD.csv

backfill_bridge_paged.go:
docker compose run --rm bot go run /app/tools/backfill_bridge_paged.go -product BTC-USD -granularity ONE_MINUTE -limit 300 -pages 20 -out /app/data/BTC-USD.csv

Runtime Processes

Start monitoring: cd ~/coinbase/monitoring && docker compose up -d

Health checks:

Bridge: curl -s http://bridge:8787/health

Bot: curl -s http://localhost:8080/healthz

Switch to live: DRY_RUN=false in bot.env; command /usr/local/go/bin/go run . -live -interval 15, restart=unless-stopped

Switch to backtest: command /usr/local/go/bin/go run . -backtest /app/data/BTC-USD.csv -interval 1, restart="no", BACKTEST_SLEEP_MS=500

Kill-switch: DRY_RUN=true + restart bot, or docker compose stop bot

Operating rules:

Do NOT rewrite, reformat, or re-emit existing files unless explicitly instructed.

Default to INCREMENTAL CHANGES ONLY; ask for file context if needed.

Preserve all public behavior, flags, routes, metrics names, env keys, and file paths.

How to respond:

List Required Inputs you need (e.g., “paste current strategy.go”, “what Slack webhook URL?”, “what Docker base image?”).

Provide a brief Plan.

Deliver changes as diffs/patches, shell commands/env additions, and short runbooks.

Constraints:

Keep dependencies minimal; list precise versions if added.

Maintain metrics compatibility and logging style.

Any live-trading change must include explicit safety callout and revert instructions.

Goal:
Extend the bot safely and incrementally while implementing this Phase, without repeating or replacing the existing foundation.
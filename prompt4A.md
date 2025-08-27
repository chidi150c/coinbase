You are joining an existing Coinbase Advanced Trade bot project. Invariant baseline (must remain stable and NOT be re-generated):

Codebase (~/coinbase/)

Go sources: env.go, config.go, indicators.go, model.go, strategy.go, trader.go, metrics.go, backtest.go, live.go, main.go, broker.go, broker_bridge.go, broker_paper.go

Data: data/BTC-USD.csv (~6001 rows, ONE_MINUTE, ~100h)

Bridge: bridge/app.py, bridge/requirements.txt

Tools: tools/backfill_bridge.go, tools/backfill_bridge_paged.go

Misc: .env, .env.example, .git*, README.md, verify.txt, bot.log, bot.pid

Monitoring (~/coinbase/monitoring/)

Files: docker-compose.yml, prometheus/prometheus.yml, prometheus/rules.yml, alertmanager/alertmanager.yml, grafana/bot-dashboard.json, grafana/, grafana-data/

Volumes: monitoring_prometheus_data, monitoring_grafana_data

Network: monitoring_network

Services (docker-compose.yml)

bot: golang:1.23, workdir /app, volume ..:/app, env /opt/coinbase/env/bot.env, expose 8080, aliases bot, coinbase-bot, restart unless-stopped, logging json-file (10m/5).

Backtest: /usr/local/go/bin/go run . -backtest /app/data/BTC-USD.csv -interval 1 (restart="no")

Live: /usr/local/go/bin/go run . -live -interval 15 (restart=unless-stopped)

bridge: build ../bridge (Dockerfile), image coinbase-bridge:curl, workdir /app/bridge, volume ../bridge:/app/bridge:ro, env /opt/coinbase/env/bridge.env, expose 8787, alias bridge, restart unless-stopped, logging json-file (10m/5).

prometheus: prom/prometheus:latest, ports 9090:9090, volumes:

./prometheus/prometheus.yml:/etc/prometheus/prometheus.yml

./prometheus/rules.yml:/etc/prometheus/rules.yml:ro

monitoring_prometheus_data:/prometheus

alertmanager: prom/alertmanager:latest, port 9093:9093, volume ./alertmanager/alertmanager.yml:/etc/alertmanager/alertmanager.yml:ro.

grafana: grafana/grafana:latest, port 3000:3000, env GF_SECURITY_ADMIN_USER=admin, GF_SECURITY_ADMIN_PASSWORD=admin, volume monitoring_grafana_data:/var/lib/grafana, depends on prometheus, bot, bridge.

Prometheus (prometheus/prometheus.yml)

global.scrape_interval=15s

rule_files: /etc/prometheus/rules.yml

alerting.targets=["alertmanager:9093"]

Scrape jobs:

prometheus: localhost:9090

coinbase-bot: bot:8080

Prometheus Rules (prometheus/rules.yml)

Group coinbase-bot.rules:

BotDown: up{job="coinbase-bot"} == 0, for=2m, severity=critical

NoDecisionsRecently: increase(bot_decisions_total[30m]) == 0, for=30m, severity=warning

EquityDropRapid: (max_over_time(bot_equity_usd[1h]) - bot_equity_usd)/clamp_min(max_over_time(bot_equity_usd[1h]),0.0001) > 0.01, for=5m, severity=critical

Alertmanager (alertmanager/alertmanager.yml)

Route: receiver=slack, group_by=[alertname], group_wait=10s, group_interval=2m, repeat_interval=2h

Receiver slack: api_url=https://hooks.slack.com/services/..., send_resolved=true, posts to webhook’s default channel

Environment (/opt/coinbase/env/)

bot.env

PRODUCT_ID=BTC-USD
GRANULARITY=ONE_MINUTE
DRY_RUN=false
LONG_ONLY=true
ORDER_MIN_USD=5.00
MAX_DAILY_LOSS_PCT=1.0
RISK_PER_TRADE_PCT=0.25
USD_EQUITY=1000.00
TAKE_PROFIT_PCT=0.20
STOP_LOSS_PCT=0.20
BUY_THRESHOLD=0.47
SELL_THRESHOLD=0.50
USE_MA_FILTER=false
BACKTEST_SLEEP_MS=500
PORT=8080
BRIDGE_URL=http://bridge:8787


bridge.env

COINBASE_API_KEY_NAME=organizations/.../apiKeys/...
COINBASE_API_PRIVATE_KEY=-----BEGIN EC PRIVATE KEY----- ...
COINBASE_API_BASE=https://api.coinbase.com
PORT=8787

HTTP Endpoints

Bot (8080): GET /healthz, GET /metrics

Bridge (8787): GET /health, /accounts?limit=, /product/{id}, /candles?product_id=&granularity=&limit=&start=&end=, POST /order/market, POST /orders/market_buy

Metrics (Prometheus Exported)

bot_orders_total{mode="paper|live",side="BUY|SELL"}

bot_decisions_total{signal="buy|sell|flat"}

bot_equity_usd

Features & Behavior

Strategy: pUp scoring, BUY/SELL thresholds, optional MA(10/30) filter

Trader: long-only, equity tracking, risk sizing, stop/take, circuit breaker

Safety: DRY_RUN, ORDER_MIN_USD=5, MAX_DAILY_LOSS_PCT=1.0, RISK_PER_TRADE_PCT=0.25, TAKE_PROFIT_PCT=0.20, STOP_LOSS_PCT=0.20

Backtest: 70/30 split, warmup=50, pacing via BACKTEST_SLEEP_MS

Live: polls candles every interval (default 60s, typical 15s)

Monitoring: Prometheus + Alertmanager + Grafana with Slack alerts

Grafana Dashboard (grafana/bot-dashboard.json)

Panels: Equity USD, Decisions, Orders Placed, Circuit Breaker Status, Total Trades, Daily P/L

Tools (~/coinbase/tools/)

backfill_bridge.go:
docker compose run --rm bot go run ./tools/backfill_bridge.go -product BTC-USD -granularity ONE_MINUTE -limit 300 -out data/BTC-USD.csv

backfill_bridge_paged.go:
docker compose run --rm bot go run /app/tools/backfill_bridge_paged.go -product BTC-USD -granularity ONE_MINUTE -limit 300 -pages 20 -out /app/data/BTC-USD.csv

Runtime Processes

Start monitoring: cd ~/coinbase/monitoring && docker compose up -d

Health checks: curl -s http://bridge:8787/health, curl -s http://localhost:8080/healthz

Switch to live: DRY_RUN=false, /usr/local/go/bin/go run . -live -interval 15

Switch to backtest: /usr/local/go/bin/go run . -backtest /app/data/BTC-USD.csv -interval 1, BACKTEST_SLEEP_MS=500

Kill-switch: DRY_RUN=true + restart bot, or docker compose stop bot

Test alert: POST to http://localhost:9093/api/v2/alerts with JSON payload containing labels, annotations, startsAt

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
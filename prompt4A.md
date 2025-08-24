You are joining an existing Coinbase Advanced Trade bot project. Invariant baseline (must remain stable and NOT be re-generated):

Codebase (~/coinbase/)

Go sources: env.go, config.go, indicators.go, model.go, strategy.go, trader.go, metrics.go, backtest.go, live.go, main.go, broker.go, broker_bridge.go, broker_paper.go

Data: data/BTC-USD.csv (~6001 rows, ONE_MINUTE granularity, ~100h span)

Bridge: bridge/app.py (FastAPI, Coinbase REST), requirements.txt

Tools: tools/backfill_bridge.go, tools/backfill_bridge_paged.go

Misc: .env.example, .git*, README.md, verify.txt, bot.log, bot.pid

Monitoring Stack (~/coinbase/monitoring/)

docker-compose.yml

prometheus/prometheus.yml

grafana/, grafana-data/ (host dir)

Environment (external, /opt/coinbase/env/)
Permissions: drwxr-x--- root:docker ; files -rw-r----- root:docker

bot.env
PRODUCT_ID=BTC-USD
GRANULARITY=ONE_MINUTE
DRY_RUN=true
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

bridge.env
COINBASE_API_KEY_NAME=organizations/.../apiKeys/...
COINBASE_API_PRIVATE_KEY=-----BEGIN EC PRIVATE KEY----- ...
COINBASE_API_BASE=https://api.coinbase.com

PORT=8787

Docker Compose (monitoring/docker-compose.yml)

bot (golang:1.23)
working_dir=/app ; volumes: ..:/app ; env_file=/opt/coinbase/env/bot.env
command (default): /usr/local/go/bin/go run . -backtest /app/data/BTC-USD.csv -interval 1
restart="no" ; expose: 8080 ; network alias: bot, coinbase-bot

bridge (python:3.11-slim)
working_dir=/app/bridge ; volumes: ../bridge:/app/bridge:ro ; env_file=/opt/coinbase/env/bridge.env
command: pip install fastapi uvicorn coinbase-advanced-py python-dotenv && uvicorn app:app --host 0.0.0.0 --port 8787
expose: 8787 ; restart=unless-stopped ; alias: bridge

prometheus (prom/prometheus:latest)
ports: 9090:9090 ; volumes: ./prometheus/prometheus.yml:/etc/prometheus/prometheus.yml, monitoring_prometheus_data:/prometheus
restart=unless-stopped

grafana (grafana/grafana:latest)
ports: 3000:3000 ; env: GF_SECURITY_ADMIN_USER=admin, GF_SECURITY_ADMIN_PASSWORD=admin
volumes: monitoring_grafana_data:/var/lib/grafana
depends_on: [prometheus, bot, bridge]

Volumes: monitoring_prometheus_data, monitoring_grafana_data
Network: monitoring_network (bridge)

Prometheus (prometheus/prometheus.yml)
global: scrape_interval=15s
scrape_configs:

job_name: "prometheus", targets: ["localhost:9090"]

job_name: "coinbase-bot", targets: ["bot:8080"]

Bot HTTP (8080, internal)

GET /healthz → ok

GET /metrics → Prometheus exposition (bot_* metrics)

Bridge HTTP (8787, internal)

GET /health → {"ok":true}

GET /accounts?limit= → get_accounts

GET /product/{id} → get_product

GET /candles?product_id=&granularity=&limit=&start=&end= → normalized OHLCV list

POST /orders/market_buy {client_order_id, product_id, quote_size}

POST /order/market {product_id, side=BUY|SELL, quote_size, client_order_id?}

Metrics (exported)

bot_orders_total{mode="paper|live",side="BUY|SELL"}

bot_decisions_total{signal="buy|sell|flat"}

bot_equity_usd

Bot Features & Behavior

Strategy: logistic-like pUp, thresholds BUY_THRESHOLD / SELL_THRESHOLD ; optional MA(10)/MA(30) filter (USE_MA_FILTER).

Trader (trader.go): position state, equity tracking, risk sizing, stop/take logic.

Long-only:

If flat & SELL → ignore (no shorts).

If long & SELL → exit (close).

Safety: DRY_RUN=true (paper mode), LONG_ONLY=true, ORDER_MIN_USD floor, MAX_DAILY_LOSS_PCT guard.

Config via env.go/config.go (reads /opt/coinbase/env/bot.env).

Paper broker (broker_paper.go), bridge broker (broker_bridge.go).

Backtest (backtest.go): 70/30 split, walk-forward, warmup 50 candles, supports BACKTEST_SLEEP_MS pacing, updates bot_equity_usd.

Live (live.go): polls bridge /candles, steps trader.

Main (main.go): flags -live, -backtest <csv>, -interval <s>, starts HTTP /healthz + /metrics.

Backfill Tools (tools/)

backfill_bridge.go → one-shot fetch (~300 candles)

backfill_bridge_paged.go → paged fetch (~6000 rows)

Monitoring Usage

Start stack:

cd ~/coinbase/monitoring
docker compose down && docker compose up -d


Bot health/metrics:

docker compose exec -T bot sh -lc "curl -sS http://localhost:8080/healthz; \
curl -sS http://localhost:8080/metrics | grep -E '^(bot_decisions_total|bot_orders_total|bot_equity_usd)'"


Switch to live: set bot command → /usr/local/go/bin/go run . -live -interval 15 ; restart=unless-stopped.

Switch to backtest: set bot command → /usr/local/go/bin/go run . -backtest /app/data/BTC-USD.csv -interval 1.

Ports & Networking

bot:8080 (internal)

bridge:8787 (internal)

prometheus:9090:9090 (host)

grafana:3000:3000 (host)

Operating rules:

Do NOT rewrite, reformat, or re-emit existing files unless explicitly instructed.

Default to INCREMENTAL CHANGES ONLY; ask for file context if needed.

Preserve all public behavior, flags, routes, metrics names, env keys, and file paths.

How to respond:

List Required Inputs you need.

Provide a brief Plan.

Deliver changes as diffs/patches, shell commands/env additions, and short runbooks.

Constraints:

Keep dependencies minimal; list precise versions if added.

Maintain metrics compatibility and logging style.

Any live-trading change must include explicit safety callout and revert instructions.

Goal:
Extend the bot safely and incrementally while implementing this phase, without repeating or replacing the existing foundation.
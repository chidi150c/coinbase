Paths & Key Files

Repo root ~/coinbase (key files only): main.go, trader.go, broker.go, broker_bridge.go, broker_paper.go, config.go, env.go, metrics.go, model.go, strategy.go, indicators.go, live.go, backtest.go, tools/backfill_bridge_paged.go

Bridge (FastAPI): bridge/app.py, bridge/Dockerfile, bridge/requirements.txt

Monitoring: monitoring/docker-compose.yml, monitoring/prometheus/prometheus.yml, monitoring/alertmanager/, monitoring/grafana/, monitoring/grafana-data/

Container build: Dockerfile (bot), .dockerignore, Makefile

CI/CD: .github/workflows/ci.yml, .github/workflows/docker.yml, .github/workflows/deploy.yml

VM runtime dirs (mounted): /opt/coinbase/env/, /opt/coinbase/state/ (persisted state file via STATE_FILE=/opt/coinbase/state/bot_state.json)

Bot Runtime (Go, :8080)

HTTP surfaces: :8080 → /healthz, /metrics

Core loop: synchronized step(); opens/holds/exits; mutex around state; broker I/O unlocked; daily UTC reset

State & persistence: equity/PnL/lots saved atomically to STATE_FILE

Orders: PlaceMarketQuote(...) (live) or PaperBroker (paper); fee- and fill-aware PnL (uses Price, BaseSize, CommissionUSD when present)

Runner & pyramiding: 1st lot is runner (TP×2, SL×1); adds gated by spacing/adverse move with time-decay; newest surviving lot promoted runner on runner close

Trailing (runner-only): activates at TRAIL_ACTIVATE_PCT; trails by TRAIL_DISTANCE_PCT

Long-only: discretionary SELL entries ignored while lots are open; exits via TP/SL/trailing only

Daily breaker: MAX_DAILY_LOSS_PCT enforced; paper price mirror kept in sync

Insufficient funds fallback (live open): on open failure matching insufficient-funds, retry once with min:

WARN log (exact): [WARN] open order %.2f USD failed (%v); retrying with ORDER_MIN_USD=%.2f

Strategy & Model

Signals: logistic baseline/extended; optional MA filter; thresholds via BUY_THRESHOLD, SELL_THRESHOLD

Indicators: SMA/RSI/ZScore

Volatility risk adjust: multiplicative factor via recent rel-vol; exported metric

Configuration (env keys)

Trading target/cadence: PRODUCT_ID, GRANULARITY, USE_TICK_PRICE, TICK_INTERVAL_SEC, CANDLE_RESYNC_SEC

Risk & sizing: ORDER_MIN_USD, MAX_DAILY_LOSS_PCT, RISK_PER_TRADE_PCT, USD_EQUITY, MAX_HISTORY_CANDLES

Position controls: DRY_RUN, LONG_ONLY, ALLOW_PYRAMIDING, PYRAMID_MIN_ADVERSE_PCT, PYRAMID_DECAY_LAMBDA, PYRAMID_MIN_SECONDS_BETWEEN, MAX_CONCURRENT_LOTS, PYRAMID_DECAY_MIN_PCT

Scalp TP decay (optional): SCALP_TP_DECAY_ENABLE, SCALP_TP_DEC_MODE, SCALP_TP_DEC_PCT, SCALP_TP_DECAY_FACTOR, SCALP_TP_MIN_PCT

Exits (incl. runner trailing): TAKE_PROFIT_PCT, STOP_LOSS_PCT, TRAIL_ACTIVATE_PCT, TRAIL_DISTANCE_PCT

Fees/state: FEE_RATE_PCT, STATE_FILE=/opt/coinbase/state/bot_state.json

Strategy thresholds/ops: BUY_THRESHOLD, SELL_THRESHOLD, USE_MA_FILTER, BACKTEST_SLEEP_MS, PORT=8080, BRIDGE_URL=http://bridge:8787, USE_LIVE_EQUITY, MODEL_MODE=extended, WALK_FORWARD_MIN, VOL_RISK_ADJUST, DAILY_BREAKER_MARK_TO_MARKET, SLACK_WEBHOOK (optional)

Bridge Service (:8787)

HTTP surfaces: :8787 → GET /health, GET /accounts?limit=, GET /product/{id}, GET /candles?granularity&limit&product_id (≤350), GET /price?product_id=, POST /orders/market_buy, POST /order/market

WebSocket (optional): caches latest ticks for COINBASE_WS_PRODUCTS

Env: COINBASE_API_KEY_NAME, COINBASE_API_PRIVATE_KEY, COINBASE_API_BASE=https://api.coinbase.com, PORT=8787; WS toggles: COINBASE_WS_ENABLE, COINBASE_WS_PRODUCTS, COINBASE_WS_URL=wss://advanced-trade-ws.coinbase.com, COINBASE_WS_STALE_SEC

Monitoring Stack (Compose/Prometheus/Grafana)

Compose (monitoring/docker-compose.yml):

Network: monitoring_network (bridge)

Volumes: monitoring_prometheus_data, monitoring_grafana_data

Services/images:

bot: ghcr.io/<owner>/coinbase-bot:latest; entrypoint /app/bot; args ["-live","-interval","1"]; env env_file: /opt/coinbase/env/bot.env; mounts /opt/coinbase/env:ro, /opt/coinbase/state; expose 8080; logs json-file

bridge: ghcr.io/<owner>/coinbase-bridge:latest; env env_file: /opt/coinbase/env/bridge.env; expose 8787

prometheus: prom/prometheus:latest; 9090:9090; volumes ./prometheus/prometheus.yml:/etc/prometheus/prometheus.yml, monitoring_prometheus_data:/prometheus; flag --storage.tsdb.retention.size=2GB

alertmanager: prom/alertmanager:latest; 9093:9093; volume ./alertmanager/alertmanager.yml:/etc/alertmanager/alertmanager.yml:ro

grafana: grafana/grafana:latest; 3000:3000; env GF_SECURITY_ADMIN_USER=admin, GF_SECURITY_ADMIN_PASSWORD=admin; volume monitoring_grafana_data:/var/lib/grafana

Prometheus config (monitoring/prometheus/prometheus.yml):

Jobs/targets: prometheus → localhost:9090; coinbase-bot → bot:8080

Bot metrics (names stable): bot_orders_total{mode,side}, bot_decisions_total{signal}, bot_trades_total{result=open|win|loss}, bot_equity_usd, bot_model_mode{mode}, bot_vol_risk_factor, bot_walk_forward_fits_total

Backtest

Source: backtest.go; input CSV data/BTC-USD.csv (~1m candles)

Defaults: train/test 70/30; warmup 50; pacing via BACKTEST_SLEEP_MS; uses MAX_HISTORY_CANDLES

Behavior: DRY_RUN disables live orders; metrics updated same as live

Minimal Ops Commands (VM)

Roll stack (VM):

cd /home/chidi/coinbase && git fetch --all && git reset --hard origin/main

cd /home/chidi/coinbase/monitoring && docker compose up -d --pull=always --force-recreate && docker image prune -f

In-network health checks (VM):

docker run --rm --network=monitoring_monitoring_network curlimages/curl:8.8.0 curl -fsS http://bot:8080/healthz && echo "bot OK"

docker run --rm --network=monitoring_monitoring_network curlimages/curl:8.8.0 curl -fsS http://bridge:8787/health && echo "bridge OK"

Verify images/containers (VM):

docker compose images | grep ghcr.io

docker compose ps

CI/CD (Appendix)

Build & push (.github/workflows/docker.yml): on push to main; login GHCR with ${{ secrets.GITHUB_TOKEN }}; build/push ghcr.io/${{ github.repository_owner }}/coinbase-bot:{latest,${{ github.sha }}} and .../coinbase-bridge:{latest,${{ github.sha }}} (linux/amd64)

Tests (.github/workflows/ci.yml): go vet ./... and go test -count=1 ./...

Deploy (.github/workflows/deploy.yml): on successful workflow_run of “Build & Push Images” (branch main); SSH to VM using SSH_PRIVATE_KEY, SSH_HOST, SSH_USER; repo var DEPLOY_DIR (e.g., /home/chidi/coinbase/monitoring); remote script:

Ensure clone at /home/chidi/coinbase (HTTPS); git fetch --all && git reset --hard origin/main

cd "$DEPLOY_DIR" && docker compose up -d --pull=always --force-recreate && docker image prune -f

Optional GHCR login on VM if packages private: GHCR_PAT (else public)
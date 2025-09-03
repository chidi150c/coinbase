9) Runbooks

Start:

cd ~/coinbase/monitoring
docker compose up -d


Health checks:

docker compose exec bot sh -lc 'curl -s http://localhost:8080/healthz'
docker compose exec bot sh -lc 'curl -s http://bridge:8787/health'


Metrics check:

docker compose exec bot sh -lc 'curl -s http://localhost:8080/metrics | egrep "bot_equity_usd|bot_decisions_total|bot_orders_total"'


Backfill CSV:

docker compose run --rm bot go run /app/tools/backfill_bridge_paged.go \
  -product BTC-USD -granularity ONE_MINUTE -limit 300 -pages 20 -out /app/data/BTC-USD.csv


Backtest:

docker compose run --rm bot /usr/local/go/bin/go run . \
  -backtest /app/data/BTC-USD.csv -interval 1


Live:

ensure DRY_RUN=false in /opt/coinbase/env/bot.env
docker compose up -d bot


Kill-switch:

docker compose stop bot
or set DRY_RUN=true and restart


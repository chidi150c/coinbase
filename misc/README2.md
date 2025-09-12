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
===============================================


# Always run from the VM
set -euo pipefail
cd /home/chidi/coinbase
git fetch --all && git reset --hard origin/main
cd monitoring
docker compose up -d --pull=always --force-recreate
docker image prune -f

# Container statuses
docker compose ps

# Images used (bot/bridge should be from GHCR)
docker compose images | grep ghcr.io

# In-network health check for the bot
docker run --rm --network=monitoring_monitoring_network curlimages/curl:8.8.0 \
  curl -fsS http://bot:8080/healthz && echo "bot OK"

# bot
docker image inspect ghcr.io/chidi150c/coinbase-bot:latest \
  --format '{{ index .Config.Labels "org.opencontainers.image.revision"}}  {{.Id}}'

# bridge
docker image inspect ghcr.io/chidi150c/coinbase-bridge:latest \
  --format '{{ index .Config.Labels "org.opencontainers.image.revision"}}  {{.Id}}'

====================================

chidi@Dynamo:~/coinbase$ cd /home/chidi/coinbase/monitoring
docker compose run --rm \
  -e BACKTEST_SLEEP_MS=10 \
  -e STOP_LOSS_PCT=0.6 \
  bot go run /app/backtest.go

The command 'docker' could not be found in this WSL 2 distro.
We recommend to activate the WSL integration in Docker Desktop settings.

For details about using Docker Desktop with WSL 2, visit:

https://docs.docker.com/go/wsl2/

chidi@Dynamo:~/coinbase/monitoring$ 

=================================================
# SET (enable backtest-friendly gates)
# Run on the VM
sudo sed -i -E \
  -e '/^PYRAMID_MIN_SECONDS_BETWEEN=/d' \
  -e '/^PYRAMID_DECAY_LAMBDA=/d' \
  /opt/coinbase/env/bot.env
printf "PYRAMID_MIN_SECONDS_BETWEEN=0\nPYRAMID_DECAY_LAMBDA=0\n" | sudo tee -a /opt/coinbase/env/bot.env >/dev/null

# (optional) verify
grep -E '^(PYRAMID_MIN_SECONDS_BETWEEN|PYRAMID_DECAY_LAMBDA)=' /opt/coinbase/env/bot.env

===============

# RESET (return to live defaults)
# Run on the VM
sudo sed -i -E \
  -e '/^PYRAMID_MIN_SECONDS_BETWEEN=/d' \
  -e '/^PYRAMID_DECAY_LAMBDA=/d' \
  /opt/coinbase/env/bot.env

# (optional) verify – should print nothing (defaults apply = 0)
grep -E '^(PYRAMID_MIN_SECONDS_BETWEEN|PYRAMID_DECAY_LAMBDA)=' /opt/coinbase/env/bot.env || echo "both unset (defaults=0)"
======================================================================
cd /home/chidi/coinbase/monitoring

# 1) Generate ~6000 rows (300 candles × 20 pages)
docker compose run --rm --no-deps \
  bot /usr/local/go/bin/go run /app/tools/backfill_bridge_paged.go \
  -product BTC-USD -granularity ONE_MINUTE -limit 300 -pages 20 \
  -out /app/data/BTC-USD-live-sample.csv

# 2) Row count (excludes header)
docker compose run --rm --no-deps bot sh -lc \
  'tail -n +2 /app/data/BTC-USD-live-sample.csv | wc -l'

# 3) Min/Max CLOSE (col 5)
docker compose run --rm --no-deps bot sh -lc \
  'awk -F, "NR>1{c=\$5; if(min==\"\"||c<min)min=c; if(max==\"\"||c>max)max=c} END{printf(\"min_close=%s\\nmax_close=%s\\n\",min,max)}" /app/data/BTC-USD-live-sample.csv'

==============================

# Count how often the adverse gate blocked adds
docker compose logs -t bot | grep -c 'pyramid: blocked by last gate'

# Confirm backtest envs are in effect (container must be running)
docker compose exec -T bot sh -lc 'env | egrep "PYRAMID_|LONG_ONLY|TAKE_PROFIT_PCT|STOP_LOSS_PCT|RISK_PER_TRADE_PCT|MODEL_MODE|WALK_FORWARD_MIN"'

docker compose exec -T bot sh -lc 'env | egrep "SCALP_TP_DEC|TAKE_PROFIT_PCT|ALLOW_PYRAMIDING"'

===========================================

# Count by reason
docker compose logs bot | awk -F'reason=' '/ EXIT /{split($2,a," "); r=a[1]; c[r]++} END{for(k in c) printf "%s %d\n", k, c[k]}'

# Count by reason AND whether P/L >= 0 (win) or < 0 (loss)
docker compose logs bot | awk -F'reason=| P/L=' '/ EXIT /{
  split($2,a," "); r=a[1];
  pl=$3+0;
  res=(pl>=0?"win":"loss");
  key=r"|"res; c[key]++
} END{for(k in c) printf "%s %d\n", k, c[k]}'




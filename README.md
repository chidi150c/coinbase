coinbase

AI-assisted crypto trading bot in Go with a local Python FastAPI bridge to Coinbase Advanced Trade.
Safe-first (paper → guarded live), observable (health + logs), and risk-guarded (daily loss caps, tiny order sizes).

⚠️ Trading crypto is risky. Use read-only keys while developing, and when you go live, keep order sizes tiny and withdrawals disabled.

Contents

Architecture

Repo layout

WSL vs PowerShell (which to use?)

Quick setup — WSL2 (recommended)

Project bootstrap

Python bridge (FastAPI)

Verify credentials via the bridge

Environment & best practices

Bridge API

Troubleshooting

Security checklist

Next steps

Architecture

Go bot: strategy/logic, risk controls, metrics.

Python sidecar (FastAPI): wraps coinbase-advanced-py and handles all Coinbase auth and REST calls.

verify.go: Go utility that calls only the local bridge to confirm credentials and market access.

All Coinbase calls go through http://127.0.0.1:<port> → never expose externally.

Repo layout
~/coinbase
├─ bridge/
│  ├─ app.py                # FastAPI bridge (loads ../.env)
│  ├─ requirements.txt      # fastapi, uvicorn, python-dotenv, coinbase-advanced-py
│  └─ .venv/                # Python virtualenv (optional but recommended)
├─ .env                     # Your secrets (not committed)
├─ .env.example             # Template
├─ verify.go                # Calls bridge: /health, /accounts, /product/BTC-USD
├─ go.mod / go.sum          # Go module files
└─ README.md

WSL vs PowerShell (which to use?)

Use WSL2 if you can — it matches Linux servers and makes ops (curl, journald/systemd, Prom/Grafana) smooth.

Pick WSL2 if:

You’ll deploy on Linux (cloud/VPS/VM) later.

You prefer bash/grep/curl and .env + export workflows.

Pick PowerShell if:

You want zero extra installs, 100% Windows-native.

You’ll run as a Windows Service / Task Scheduler and don’t need Linux tooling.

Both work for the Go bot. Default recommendation: WSL2.

Quick setup — WSL2 (recommended)

Install WSL & Ubuntu:

wsl --install -d Ubuntu


Inside Ubuntu:

sudo apt-get update
sudo apt-get install -y golang git make curl chrony
go version

GitHub push via SSH (avoid HTTPS prompts)

Cloning over HTTPS doesn’t log you in; pushing needs auth. Use SSH for this repo.

# 1) Confirm repo
cd ~/coinbase
git remote -v   # should show https://github.com/chidi150c/coinbase.git
git status

# 2) Create SSH key (skip if you already have one)
ssh-keygen -t ed25519 -C "yourusername@gmail.com"
eval "$(ssh-agent -s)"
ssh-add ~/.ssh/id_ed25519
cat ~/.ssh/id_ed25519.pub   # copy this

# 3) Add to GitHub (Settings → SSH and GPG keys → New SSH key)

# 4) Switch this repo to SSH
git remote set-url origin git@github.com:chidi150c/coinbase.git
git remote -v

# 5) Push
git branch -M main
git push -u origin main


Why this works: HTTPS needs a token helper. SSH uses your key → no prompts, fewer Windows/WSL quirks.

Project bootstrap
cd ~/coinbase

# (If not already done)
go mod init github.com/chidi150c/coinbase

# .env template
cat > .env.example <<'EOF'
COINBASE_API_KEY_NAME=
COINBASE_API_PRIVATE_KEY=
COINBASE_API_BASE=https://api.coinbase.com
PRODUCT_ID=BTC-USD
GRANULARITY=ONE_MINUTE
DRY_RUN=true
MAX_DAILY_LOSS_PCT=1.0
RISK_PER_TRADE_PCT=0.25
USD_EQUITY=1000.00
TAKE_PROFIT_PCT=0.8
STOP_LOSS_PCT=0.4
ORDER_MIN_USD=5.00
PORT=8080
EOF

cp .env.example .env
chmod 600 .env
echo ".env" >> .gitignore

# (Optional) extra Go tools
go install golang.org/x/tools/cmd/goimports@latest
go install honnef.co/go/tools/cmd/staticcheck@latest


A .env file is just text; the OS won’t read it automatically. Your app reads os.Getenv(...), so you either export vars before running or have the bridge load .env for itself (it does).

Python bridge (FastAPI)

Create and activate a venv, then install deps:

mkdir -p bridge
python3 -m venv bridge/.venv
source bridge/.venv/bin/activate
pip install -U pip coinbase-advanced-py fastapi uvicorn python-dotenv


Start the bridge on localhost (do not bind publicly):

uvicorn bridge.app:app --host 127.0.0.1 --port 8787 --reload

Verify credentials via the bridge

In another terminal:

cd ~/coinbase
go run verify.go


Expected:

GET /health → 200 with {"ok": true}

GET /accounts?limit=1 → 200 with an accounts object

GET /product/BTC-USD → 200 with product/ticker snapshot

Final line: ✅ Bridge reachable; credentials verified via /accounts.

============================================================================================
AI-Assisted Coinbase Trading Bot (Go) + FastAPI Sidecar
============================================================================================

A safe, monitored, AI-assisted spot-trading bot for Coinbase Advanced Trade.
The Go bot handles strategy, risk, metrics, and logging. A small FastAPI “sidecar” wraps the official Python coinbase.rest.RESTClient and exposes a minimal HTTP surface the bot calls.

Safety first: defaults are paper mode (DRY_RUN=true), long-only, tiny notional, and a daily loss circuit breaker.

Architecture
+-------------------+          HTTP (localhost)
|   Go Trading Bot  |  <------------------------------------+
| - strategy/model  |                                        |
| - risk mgmt       |         +---------------------------+  |
| - Prometheus      | ----->  | FastAPI Sidecar (Python)  |  |
| - reads .env      |         | - coinbase.rest client    |  |
+-------------------+         | - /product /accounts ...  |  |
        ^                     | - /candles /order/market  |  |
        |                     +---------------------------+  |
        +--------------------------------- Coinbase Advanced +

Requirements

Go 1.21+

Python 3.10+

pip install fastapi uvicorn pydantic coinbase-advanced-py python-dotenv

Environment (.env)

No shell export needed. The Go bot auto-loads only the keys it needs; the Python sidecar reads the full .env (including your multiline PEM).

Example .env:

# Bot
PRODUCT_ID=BTC-USD
GRANULARITY=ONE_MINUTE
DRY_RUN=true
LONG_ONLY=true
USD_EQUITY=1000.00
RISK_PER_TRADE_PCT=0.25
MAX_DAILY_LOSS_PCT=1.0
TAKE_PROFIT_PCT=0.8
STOP_LOSS_PCT=0.4
ORDER_MIN_USD=5.00
PORT=8080
BRIDGE_URL=http://127.0.0.1:8787

# Strategy thresholds (tunable without rebuild)
BUY_THRESHOLD=0.55
SELL_THRESHOLD=0.45
USE_MA_FILTER=true

# Sidecar / Coinbase API
COINBASE_API_KEY_NAME=organizations/.../apiKeys/...
COINBASE_API_PRIVATE_KEY=-----BEGIN PRIVATE KEY-----\n...multi-line PEM...\n-----END PRIVATE KEY-----
# If your PEM is a single line with \n literals, the sidecar converts them.


⚠️ Keep BRIDGE_URL clean (no trailing comments on the same line). If you add # ... after the URL, the HTTP client may fail to parse it.

Sidecar (FastAPI) — endpoints

Your sidecar exposes:

GET /health → {"ok": true}

GET /accounts?limit=<n>

GET /product/{product_id}

GET /candles?product_id=BTC-USD&granularity=ONE_MINUTE&limit=300 → normalized OHLCV array

POST /orders/market_buy (legacy BUY)

POST /order/market (new unified BUY/SELL; body: {product_id, side: BUY|SELL, quote_size: "5.00", client_order_id?})

Run it:

uvicorn app:app --host 127.0.0.1 --port 8787 --reload


Sanity checks:

curl -s http://127.0.0.1:8787/health
curl -s "http://127.0.0.1:8787/accounts?limit=1"
curl -s "http://127.0.0.1:8787/product/BTC-USD" | python3 -m json.tool | head
curl -s "http://127.0.0.1:8787/candles?product_id=BTC-USD&granularity=ONE_MINUTE&limit=5" | python3 -m json.tool | head

Verify bridge + credentials (Go)

verify.go hits the sidecar only:

go run verify.go
# Expect:
# GET http://127.0.0.1:8787/health -> 200 {"ok":true}
# GET .../accounts?limit=1 -> 200 {...}
# GET .../product/BTC-USD -> 200 {...}

Run the bot

Foreground (paper by default):

go run . -live -interval 15


Background with logs:

nohup bash -lc 'go run . -live -interval 15' > bot.log 2>&1 & echo $! > bot.pid
tail -f bot.log


Backtest:

go run . -backtest path/to/candles.csv
# CSV headers: time|timestamp, open, high, low, close, volume
# time can be RFC3339 or UNIX seconds


Flip to live trading (guarded):

Set DRY_RUN=false in .env

Keep LONG_ONLY=true, ORDER_MIN_USD=5.00, MAX_DAILY_LOSS_PCT=1.0

Restart the bot

Metrics & health

Health: curl -s localhost:${PORT}/healthz → ok

Prometheus: curl -s localhost:${PORT}/metrics | head

Metrics exposed:

bot_orders_total{mode="paper|live", side="BUY|SELL"}

bot_decisions_total{signal="buy|sell|flat"}

bot_equity_usd

Configuration knobs (quick reference)

Product: PRODUCT_ID, GRANULARITY (ONE_MINUTE, FIVE_MINUTE, …)

Safety: DRY_RUN, LONG_ONLY, ORDER_MIN_USD, MAX_DAILY_LOSS_PCT

Sizing: USD_EQUITY, RISK_PER_TRADE_PCT

Stops/Targets: STOP_LOSS_PCT, TAKE_PROFIT_PCT

Strategy thresholds: BUY_THRESHOLD, SELL_THRESHOLD, USE_MA_FILTER

Ops: PORT (metrics/health), BRIDGE_URL (sidecar)

Project layout (Option A: single package)
.
├── app.py                      # FastAPI sidecar
├── verify.go                   # bridge/creds sanity checker
├── env.go                      # .env loader + helpers
├── config.go                   # Config struct + loader
├── indicators.go               # SMA, RSI, ZScore
├── model.go                    # tiny logistic-like micro-model
├── strategy.go                 # decide() logic
├── broker.go                   # Broker interface + types
├── broker_bridge.go            # HTTP client to sidecar
├── broker_paper.go             # paper broker
├── trader.go                   # state, risk, synchronized step()
├── metrics.go                  # Prometheus collectors
├── backtest.go                 # CSV loader + backtest runner
├── live.go                     # real-time loop, candle polling
└── main.go                     # flags, wiring, HTTP server

Troubleshooting

“invalid port” constructing URL
Remove trailing comments/spaces from BRIDGE_URL. It must be just the URL.

Port already in use
kill $(lsof -t -i:${PORT}) or change PORT in .env.

401 on /accounts
API key not authorized or IP allowlist mismatch.

Multiline PEM issues
Keep the PEM in .env exactly as a single line with \n sequences; the sidecar expands them automatically.

============================================================================================

# 5) Restart Prometheus cleanly
docker compose down
docker compose up -d prometheus

# get the IP address of the WSL VM
ip -4 addr show eth0 | awk '/inet /{print $2}' | cut -d/ -f1

cd ~/coinbase/monitoring
docker compose down
docker compose up -d --build

# Prometheus Targets
# -> http://localhost:9090/targets   (coinbase-bot should be UP)

# Quick queries (Prometheus /graph)
# bot_decisions_total
# bot_orders_total
# bot_equity_usd

1. Find the running Go process
ps aux | grep '[g]o run'


or, if you compiled it:

ps aux | grep '[c]oinbase'


That will show the PID(s) of any leftover bot processes.

2. Kill the process

Example (replace 12345 with the PID you saw):

kill -9 12345


If multiple:

pkill -f "go run"
pkill -f "coinbase"

3. Verify it’s gone
ps aux | grep coinbase

1. Make the env directory
sudo mkdir -p /opt/coinbase/env
sudo chown root:root /opt/coinbase/env
sudo chmod 700 /opt/coinbase/env

2. Create bot.env
sudo nano /opt/coinbase/env/bot.env
sudo nano /opt/coinbase/env/bridge.env

Paste this content:

Beautiful — everything rebuilt and started cleanly 🎉

Do these quick verifications:

Prometheus targets
Open: http://localhost:9090/targets

Expect:

prometheus → UP

coinbase-bot → UP (scraping bot:8080)

Prometheus queries (http://localhost:9090/graph
)
Run:

bot_decisions_total

bot_orders_total

bot_equity_usd

Grafana login
http://localhost:3000
 (admin / admin on first run)

Optional logs (only if something’s off)

docker logs coinbase-bot --tail=100
docker logs coinbase-bridge --tail=100
docker logs prometheus --tail=100


If coinbase-bot isn’t UP yet, tell me the status message shown on the Targets page (e.g., DNS fail, connection refused, etc.) and I’ll zero in on it. Otherwise, you’re done with the WSL2↔Docker networking saga — the stack is now production-style with secure env files and internal-only service wiring.

# 1) See the exact error from the bot container
docker compose logs --no-color --tail=200 bot

# 2) Quick DNS sanity from Prometheus container (optional)
docker compose exec prometheus sh -lc 'getent hosts bot || echo "no getent"; echo ---; (wget -qO- http://bot:8080/metrics | head) || echo "wget failed"'


docker logs coinbase-bot --tail=50

==========================================================================================

Coinbase Monitoring Stack – Troubleshooting Procedure
1. Manage containers

Bring the stack down and back up (clean build):

cd ~/coinbase/monitoring
docker compose down
docker compose up -d --build


Check running containers:

docker compose ps

2. Inspect logs

Follow logs for a specific service:

docker logs -f coinbase-bot
docker logs -f coinbase-bridge
docker logs -f prometheus
docker logs -f grafana


Tail last 50 lines:

docker logs coinbase-bot --tail=50

3. Restart a single service

Stop + remove only the bot, then rebuild it:

docker compose stop bot
docker compose rm -f bot
docker compose up -d --build bot

4. Debug connectivity inside containers

Enter a container shell:

docker exec -it coinbase-bot /bin/sh


Test connectivity to bridge:

curl http://bridge:8787/health
curl http://bridge:8787/candles?granularity=ONE_MINUTE&limit=2&product_id=BTC-USD

5. Verify Prometheus scraping

Open Prometheus targets page in browser:

http://localhost:9090/targets


Or query from CLI inside Prometheus container:

docker exec -it prometheus /bin/sh
curl http://bot:8080/metrics

6. Environment & configuration edits

Edit bot environment file:

sudo nano /opt/coinbase/env/bot.env


Edit Prometheus config:

sudo nano ~/coinbase/monitoring/prometheus.yml


✅ With these, you have a full troubleshooting workflow:

Rebuild stack

Check container status

Inspect logs for errors

Restart individual services

Test inter-service connectivity

Verify Prometheus targets

Adjust environment/config files

sudoedit /opt/coinbase/env/bot.env
sudoedit /opt/coinbase/env/bridge.env
docker compose up -d --build bot bridge

docker exec -it grafana curl http://prometheus:9090/-/healthy
docker exec -it coinbase-bot curl http://localhost:8080/metrics

================================================================================================
More About Paper Mode
================================================================================================

In our setup, paper mode means the bot runs end-to-end exactly like live trading except it doesn’t place real orders. Orders are simulated inside the bot’s paper broker and we still pull real market data via the bridge.

What paper mode does (in our bot)

Real-time market data: fetched from the FastAPI bridge (/candles, /product, etc.).

Signals & risk checks: the strategy, MA filters, thresholds, and safeties all run the same.

Order simulation: when the strategy decides to trade, it calls the broker interface; with DRY_RUN=true, the paper broker is used (no Coinbase order is sent).

Portfolio tracking: paper broker updates a simulated USD cash balance and position; fills are typically modeled as immediate market fills near the quote price (implementation in broker_paper.go).

Monitoring: Prometheus shows:

bot_decisions_total{signal="buy|sell|flat"}

bot_orders_total{mode="paper",side="BUY|SELL"} (increments on simulated fills)

bot_equity_usd (paper equity curve)

Safety rails still apply: LONG_ONLY=true, ORDER_MIN_USD, MAX_DAILY_LOSS_PCT stop new orders or size them down in paper just like live.

Why it’s useful

Safe forward testing: run against live prices without risking funds.

Tune thresholds: adjust env knobs and watch decisions/orders/equity in Grafana.

Ops rehearsal: validates bridge, env, logging, metrics, dashboards, and alerts.

What paper mode can’t prove (limitations)

Execution reality: no real slippage, spread widening, or partial fills; paper assumes “ideal” market-order fills.

Exchange constraints: won’t hit real rate limits, auth errors, order throttles, maintenance windows, or venue outages.

Liquidity effects: doesn’t account for your order moving the market; depth isn’t modeled.

Fees & rebates: unless explicitly modeled, fees may be ignored or approximated.

Balance/transfer flows: no actual fiat/crypto settlement, reserving, or withdrawal holds.

Infra edge cases: won’t surface cardinals like webhook callbacks, cancel/replace races, or Coinbase idiosyncrasies.

Typical knobs you can tweak in paper (examples)

(These are examples—use the actual env keys your strategy.go and model.go read.)

Probability/thresholds: e.g., PUP_BUY_THRESH=0.53, PUP_SELL_THRESH=0.47

MA filter: MA_FILTER_ENABLED=true|false, MA_FAST=10, MA_SLOW=30

Trade sizing caps: ORDER_MIN_USD=5.00, MAX_DAILY_LOSS_PCT=1.0

Interval: -interval 15 (seconds between decisions)

If you want to SEE bot_orders_total increment now (still safe)

Temporarily make buys easier (stay long-only):

sudo sed -i 's/^LONG_ONLY=.*/LONG_ONLY=true/' /opt/coinbase/env/bot.env
sudo sed -i 's/^DRY_RUN=.*/DRY_RUN=true/' /opt/coinbase/env/bot.env
# Example: lower buy threshold; disable MA filter if you have such flags
# (replace with your real keys)
# echo 'PUP_BUY_THRESH=0.52' | sudo tee -a /opt/coinbase/env/bot.env
# echo 'MA_FILTER_ENABLED=false' | sudo tee -a /opt/coinbase/env/bot.env

cd ~/coinbase/monitoring
docker compose up -d --build bot


Watch:

Prometheus: increase(bot_orders_total[5m])

Logs: docker logs -f coinbase-bot

Graduating to live (only when you explicitly opt in)

Set API keys in /opt/coinbase/env/bot.env and /opt/coinbase/env/bridge.env.

Flip DRY_RUN=false (keep LONG_ONLY=true if you want).

Keep ORDER_MIN_USD small and MAX_DAILY_LOSS_PCT protective.

Kill switch: docker compose stop bot (or revert DRY_RUN=true) immediately restores paper mode.

If you want, paste the small section of your strategy.go that reads envs and I’ll give precise key names + safe example values tailored to your bot for paper-mode tuning.


================================================================================================
docker exec -it coinbase-bot bash
go run . -backtest /data/candles.csv


cd ~/coinbase/monitoring

# Stop the always-on bot so logs are clean (Prom/Grafana/bridge stay up)
docker compose stop bot

# Sanity: show CSV header you already verified
docker compose run --rm --no-deps bot \
  /bin/sh -lc 'head -n 3 /app/data/BTC-USD.csv'

# Run the backtest (safe sandbox)
docker compose run --rm --no-deps bot \
  /usr/local/go/bin/go run . -backtest /app/data/BTC-USD.csv -interval 1


Verify

Bot logs should show more frequent FLAT→BUY transitions:

docker logs -f coinbase-bot | sed -n 's/.*\(BUY\|FLAT\|SELL\).*/\0/p'


Prometheus queries:

sum by (signal) (increase(bot_decisions_total[5m]))

sum by (side,mode) (increase(bot_orders_total[5m]))

Grafana “Orders per 5m (mode/side)” should begin to show bars (BUY/paper).


A) Backtest (Prometheus-visible) — no code changes

We’ll run backtest as the bot service (so Prometheus keeps scraping bot:8080), using a one-time compose override.

cd ~/coinbase/monitoring

# 1) Create a temporary override to switch bot into backtest mode
cat > docker-compose.override.yml <<'YAML'
services:
  bot:
    command: ["/usr/local/go/bin/go", "run", ".", "-backtest", "/app/data/BTC-USD.csv", "-interval", "1"]
YAML

# 2) Bring up Prometheus/Grafana/bridge (if not already up)
docker compose up -d prometheus grafana bridge

# 3) Start the bot (now running the backtest under the service name "bot")
docker compose up -d --build bot
==============================================================
cd ~/coinbase/monitoring

# create/overwrite the override to run backtest as the bot service
cat > docker-compose.override.yml <<'YAML'
services:
  bot:
    command: ["/usr/local/go/bin/go", "run", ".", "-backtest", "/app/data/BTC-USD.csv", "-interval", "1"]
YAML

# sanity check
echo "---- override ----"
cat docker-compose.override.yml
==========================================================
docker compose up -d prometheus grafana bridge
docker compose up -d --build bot
docker logs -f coinbase-bot
============================================================
# Edit bot env (add or update these keys)
sudo sed -i \
  -e 's/^BUY_THRESHOLD=.*/BUY_THRESHOLD=0.50/; t; $ a BUY_THRESHOLD=0.50' \
  -e 's/^SELL_THRESHOLD=.*/SELL_THRESHOLD=0.50/; t; $ a SELL_THRESHOLD=0.50' \
  -e 's/^USE_MA_FILTER=.*/USE_MA_FILTER=false/; t; $ a USE_MA_FILTER=false' \
  /opt/coinbase/env/bot.env

# sanity
grep -E 'BUY_THRESHOLD|SELL_THRESHOLD|USE_MA_FILTER' /opt/coinbase/env/bot.env


====================================================================
✅ main.go (entrypoint)

Loads env/config, builds trader, broker, model.

Starts the HTTP server (/metrics + /healthz) once on cfg.Port (default 8080).

Then branches:

runBacktest(ctx, csvBacktest, trader, model) if -backtest is set and -live is false.

Otherwise, runLive(...).

On exit, it shuts the server down gracefully
.

👉 This is correct — the metrics goroutine runs in both live and backtest. No port conflict.

✅ backtest.go

Loads candles from CSV.

Splits into train/test.

Calls model.fit(...).

Forces DryRun = true (so all trades are paper).

Loops test candles and calls trader.step(...).

This already increments bot_decisions_total{signal=…} and bot_orders_total{mode="paper",side=…} inside Trader.step.

Ends with mtxPnL.Set(trader.EquityUSD()).

No extra HTTP server code inside backtest anymore
.

👉 This is also correct — metrics are incremented during backtest, and since main.go already serves /metrics, Prometheus/Grafana can scrape them.

🔒 Invariants confirmed

Single metrics goroutine → no port conflicts.

Metrics names/labels untouched (bot_orders_total, bot_decisions_total, bot_equity_usd).

DRY_RUN=true enforced in backtest.

Backtest and live share the same /metrics endpoint.

✅ So the combination is now stable:

Run docker compose run --rm --no-deps bot /usr/local/go/bin/go run . -backtest /app/data/BTC-USD.csv -interval 1.

While that container runs, Prometheus target coinbase-bot will be UP and Grafana panels will fill.

When finished, bring back the live loop with docker compose up -d bot.
=====================================================================================
Sweet — here’s a tight runbook to backtest with metrics visible (so Prometheus/Grafana update), then return to live (paper) mode.

Backtest (one-off, scraped by Prometheus)

Uses your existing CSV at /app/data/BTC-USD.csv. We’ll stop the always-on bot, then run a temporary bot container with the service’s network alias bot so Prometheus can scrape bot:8080 while the backtest runs.

cd ~/coinbase/monitoring

# 0) Stop the always-on bot so the backtest owns :8080 and the alias "bot"
docker compose stop bot

# 1) Sanity check CSV (optional)
docker compose run --rm --no-deps bot /bin/sh -lc 'head -n 3 /app/data/BTC-USD.csv'

# 2) Run backtest as a one-off container **with service aliases**
#    --use-aliases ensures this container is reachable as "bot" on the compose network
docker compose run --rm --no-deps --use-aliases bot \
  /usr/local/go/bin/go run . -backtest /app/data/BTC-USD.csv -interval 1

What to watch (while step 2 runs)

Prometheus targets: http://localhost:9090/targets
 → coinbase-bot (bot:8080) should be UP while the backtest is running.

Prometheus queries (http://localhost:9090/graph
):

sum by (signal) (increase(bot_decisions_total[1m]))

sum by (mode, side) (increase(bot_orders_total[1m]))

bot_equity_usd

Grafana dashboard panels should begin to move during the run.

Tip: If your CSV is short, backtest may finish before Prometheus (15s interval) scrapes much. Using -interval 1 slows each step, giving Prometheus time to collect.

Return to live (paper) loop
# Bring the always-on bot back (paper-safe; DRY_RUN=true)
docker compose up -d bot

echo "BRIDGE_URL=http://bridge:8787" | sudo tee -a /opt/coinbase/env/bot.env
============================================================================
# List docker networks and look for your compose network
docker network ls | grep monitoring_monitoring_network

# Inspect it (you should see the attached containers under "Containers")
docker network inspect monitoring_monitoring_network | sed -n '1,120p'

# Check your services are up and attached
docker compose ps


========================================================================================

🔹 How to fetch new candles into BTC-USD.csv

Run this from your project root (~/coinbase): 

 ~/coinbase/monitoring

# One-shot fetch of 6,000 latest candles into CSV
Run it inside the bot container (compose network, uses bridge:8787):

cd ~/coinbase/monitoring
docker compose run --rm bot go run /app/tools/backfill_bridge_paged.go \
  -product BTC-USD \
  -granularity ONE_MINUTE \
  -limit 300 \
  -pages 20 \
  -out /app/data/BTC-USD.csv


Verify the dataset size & a couple of lines:

docker compose run --rm bot sh -lc 'wc -l /app/data/BTC-USD.csv; head -n 3 /app/data/BTC-USD.csv; tail -n 3 /app/data/BTC-USD.csv'


Then re-run your backtest with the larger CSV:

docker compose run --rm -p 8080:8080 bot \
  /usr/local/go/bin/go run . -backtest /app/data/BTC-USD.csv -interval 1


===================================================================
safest way is to throw away everything after your last good commit and reset the working tree.

From your repo root (~/coinbase):

1. Check commit history
git log --oneline --decorate --graph -n 5


This shows the last 5 commits. Identify the hash of the last good commit (say abc1234).

2. If you just want to go back to the last commit (HEAD)
git reset --hard HEAD


This removes all uncommitted changes and restores the working tree to the last commit you made.

3. If you want to go back to a specific commit
git reset --hard abc1234


Replace abc1234 with the commit hash from step 1.

4. If you want to keep your messed-up changes somewhere (just in case)

Before resetting, you can stash them:

git stash push -m "before reset"


Then do the reset. You can recover the stash later if needed:

git stash list
git stash apply stash@{0}

5. Verify
git status
git diff


Both should show a clean tree. Run your backtest/live again:

cd ~/coinbase/monitoring
docker compose build bot
docker compose up -d bot
========================================================================

cd ~/coinbase

# compile just the bot (root main package)
go build .

# or just run it
go run . -backtest data/BTC-USD.csv -interval 1
# or
go run . -live -interval 15

cd ~/coinbase/monitoring

# Make sure envs are in place (paths from your baseline)
ls -l /opt/coinbase/env/bot.env   # DRY_RUN=true LONG_ONLY=true ...
ls -l /opt/coinbase/env/bridge.env

# Start full stack
docker compose down
docker compose up -d
docker compose ps


==============================================================
backtest mode watcher:

watch -n 1 "docker compose exec -T bot sh -lc \"curl -sS http://localhost:8080/metrics | grep -E '^(bot_equity_usd|bot_decisions_total|bot_orders_total)' || true\""
============================================================================
What’s happening now

Your bot exports counters:

bot_equity_usd → your simulated balance.

bot_decisions_total{signal="buy|sell|flat"} → how many decisions so far.

bot_orders_total{mode="paper", side="BUY|SELL"} → how many orders so far.

Prometheus stores those numbers over time.

Grafana visualizes them with queries called PromQL.


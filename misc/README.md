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
=============================================================================================

Great question 👍 Let me unpack how AI is used in your bot (based on the files we’ve walked through: strategy.go, model.go, live.go, and the env flags):

1. Tiny Logistic Model ("AI MicroModel")

File: model.go

At startup (warmup), the bot collects ~300 recent candles (OHLCV).

It extracts simple technical features (like returns, moving averages, maybe RSI) and feeds them into a tiny logistic regression model (AIMicroModel).

That logistic regression outputs a probability pUp = "probability price goes up next interval".

Parameters are fit quickly in-memory (fit or FitMiniBatch with learning rate and batch size).

This is the "AI" core: not deep learning, but a simple ML classifier updated online.

2. Decision Logic (Strategy Layer)

File: strategy.go

Each tick (candle or /price nudge), the bot queries the model for pUp.

It then applies threshold rules from your env:

If pUp > BUY_THRESHOLD → BUY

If pUp < SELL_THRESHOLD → SELL

Else → FLAT

Those thresholds are effectively the “policy knob” that translates the model’s probability into actions.

3. Extended Mode (Phase-7)

If MODEL_MODE=extended, then in addition to the tiny model, the bot builds extended feature sets (more indicators, multiple lookbacks).

It trains a slightly larger logistic regression (ExtendedLogit) in mini-batches.

This lets the model adapt intraday (e.g., walk-forward refit every 60 minutes if WALK_FORWARD_MIN=60).

Still simple ML (linear logistic regression), but with more features and retraining for drift.

4. Runtime Updates

During live trading:

Each new candle (or tick nudged candle) → update features → feed into model → get pUp.

If WALK_FORWARD_MIN > 0 → model re-fits periodically, using the most recent history (online learning).

This means the AI is not static; it keeps adapting as market conditions shift.

5. Not Used For

The AI is not directly doing position sizing, PnL management, or circuit breaking.

That’s handled by rule-based risk controls (env vars like RISK_PER_TRADE_PCT, MAX_DAILY_LOSS_PCT, pyramiding rules, etc.).

The AI only outputs directional probability (pUp), which is then interpreted by strategy thresholds.

✅ In plain words:
The AI in your bot is a lightweight logistic regression classifier that predicts whether the next price move is up or down. It’s continuously retrained on recent candles, and its output probability (pUp) is thresholded into BUY/SELL/FLAT. The AI itself doesn’t know about your balance or trades—it just gives a probability; the strategy/risk logic wraps around it.

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
==============================================================================

# Run this from your host shell
cd ~/coinbase/monitoring

# Start the services if not already running
docker compose up -d bot bridge

# Exec into bot and curl the bridge service
docker compose exec bot sh -lc 'apk add --no-cache curl >/dev/null 2>&1 || true; \
  curl -s http://bridge:8787/health; \
  echo; \
  curl -s "http://bridge:8787/accounts?limit=1"; echo'

  
==========================================================
Then reload just the bridge and verify it’s serving:

cd ~/coinbase/monitoring
docker compose up -d bridge
docker compose logs --tail=50 bridge | grep -E "Uvicorn running|Application startup complete"


Curl it from the bot container (same network):

docker compose exec bot bash -lc '
  apt-get update -qq && apt-get install -y -qq curl;
  echo "---- HEALTH ----";
  curl -s http://bridge:8787/health; echo;
  echo "---- ACCOUNTS ----";
  curl -s "http://bridge:8787/accounts?limit=1"; echo
'
=====================================================

Do exactly this
# 1) Confirm current value (should show DRY_RUN=true)
sudo awk -F= '$1=="DRY_RUN"{print "DRY_RUN="$2}' /opt/coinbase/env/bot.env

# 2) Switch to live
sudo sed -i 's/^DRY_RUN=.*/DRY_RUN=false/' /opt/coinbase/env/bot.env
sudo awk -F= '$1=="DRY_RUN"{print "DRY_RUN="$2}' /opt/coinbase/env/bot.env

# 3) Make sure compose command is live mode (should show: ... go run . -live -interval 15)
cd ~/coinbase/monitoring
docker compose ps

# 4) Recreate bot so it reads the updated env file
docker compose up -d --force-recreate --no-deps bot

# 5) Verify in logs (look for dry_run=false and the safety banner)
docker compose logs --tail=20 bot
docker compose logs -f bot | grep -E "Starting | \\[SAFETY\\]"

# or more info with

docker compose logs -f bot | grep -E "Starting|\\[SAFETY\\]|LIVE ORDER|EXIT|CIRCUIT_BREAKER|step err"

# watch metrics

watch -n 1 "docker compose exec -T bot sh -lc \"curl -sS http://localhost:8080/metrics | grep -E '^(bot_equity_usd|bot_decisions_total|bot_orders_total|bot_trades_total)' || true\""

# 6) Force a recreate of ONLY the bot (re-reads env_file)
cd ~/coinbase/monitoring
docker compose up -d --force-recreate --no-deps bot

=======================================================

# Runtime Processes

# 7) Stack start:
cd ~/coinbase/monitoring
docker compose down && docker compose up -d

# 8} Bot health/metrics:
docker compose exec -T bot sh -lc "curl -sS http://localhost:8080/healthz; \
curl -sS http://localhost:8080/metrics | grep -E '^(bot_decisions_total|bot_orders_total|bot_equity_usd)'"

# Override on the fly (no file edits)

# Docker Compose lets you override the command at runtime:

# 9)Backtest:

docker compose run --rm bot /usr/local/go/bin/go run . -backtest /app/data/BTC-USD.csv -interval 1


# 10)Live:

docker compose run --rm bot /usr/local/go/bin/go run . -live -interval 15

=======================================================
# Nice — the loop is ticking 👍 (bot_decisions_total is incrementing).
# Quick next checks to confirm live trading is wired end-to-end:
# 11)Orders & equity:

docker compose exec -T bot sh -lc 'curl -s http://localhost:8080/metrics | grep "^bot_orders_total"'
docker compose exec -T bot sh -lc 'curl -s http://localhost:8080/metrics | grep "^bot_equity_usd"'


# 12)Runtime logs (live events):

docker compose logs -f bot | grep -E "LIVE ORDER|EXIT|CIRCUIT_BREAKER"


# (Optional) Nudge a quick trade safely, then restore:
# 13)make exits quicker and buys slightly easier
sudo sed -i 's/^TAKE_PROFIT_PCT=.*/TAKE_PROFIT_PCT=0.20/' /opt/coinbase/env/bot.env
sudo sed -i 's/^STOP_LOSS_PCT=.*/STOP_LOSS_PCT=0.20/'     /opt/coinbase/env/bot.env
sudo sed -i 's/^BUY_THRESHOLD=.*/BUY_THRESHOLD=0.47/'     /opt/coinbase/env/bot.env
docker compose up -d --force-recreate --no-deps bot

# 14)after you see LIVE ORDER / EXIT:
sudo sed -i 's/^TAKE_PROFIT_PCT=.*/TAKE_PROFIT_PCT=0.8/' /opt/coinbase/env/bot.env
sudo sed -i 's/^STOP_LOSS_PCT=.*/STOP_LOSS_PCT=0.4/'     /opt/coinbase/env/bot.env
sudo sed -i 's/^BUY_THRESHOLD=.*/BUY_THRESHOLD=0.48/'    /opt/coinbase/env/bot.env
docker compose up -d --force-recreate --no-deps bot


# 15)Kill-switch / rollback:

sudo sed -i 's/^DRY_RUN=.*/DRY_RUN=true/' /opt/coinbase/env/bot.env
docker compose up -d --force-recreate --no-deps bot

================
docker compose exec -T bot sh -lc 'curl -s http://localhost:8080/healthz; echo; \
  curl -s http://localhost:8080/metrics | grep -E "^(bot_orders_total|bot_decisions_total|bot_equity_usd)"'

==================================================
# Option 2: Override on the fly (no file edits)

# Docker Compose lets you override the command at runtime:

# Backtest:

docker compose run --rm bot /usr/local/go/bin/go run . -backtest /app/data/BTC-USD.csv -interval 1


# Live:

docker compose run --rm bot /usr/local/go/bin/go run . -live -interval 15

========================================
# Flip to paper while we diagnose
sudo sed -i 's/^DRY_RUN=.*/DRY_RUN=true/' /opt/coinbase/env/bot.env
cd ~/coinbase/monitoring
docker compose restart bot
============================================================
# Bounce the stack to load Alertmanager + rules
docker compose up -d

# Verify Prometheus loaded rules
curl -s http://localhost:9090/api/v1/rules | jq '.data.groups[].name'

# Verify Alertmanager is up
curl -s http://localhost:9093/-/ready

================================================================
# Fire a test Slack alert (no impact on trading)
# Send a synthetic alert directly to Alertmanager:
START="$(date -u +%FT%TZ)"
curl -s -X POST http://localhost:9093/api/v2/alerts \
  -H 'Content-Type: application/json' \
  --data-binary @- <<JSON
[
  {
    "labels": { "alertname":"TestAlert", "severity":"info" },
    "annotations": {
      "summary":"Test alert from Alertmanager",
      "description":"Manual test to verify Slack wiring."
    },
    "startsAt": "$START"
  }
]
JSON

=====================================================

Or in a single command
docker compose down && docker compose up -d


That will:

Stop bot, bridge, prometheus, grafana, alertmanager

Recreate them with the current configs

Restart them detached

Verify afterwards
# Check container status
docker compose ps

# Bot health
curl -s http://localhost:8080/healthz

# Bridge health
curl -s http://localhost:8787/health

# Prometheus rules loaded
curl -s http://localhost:9090/api/v1/rules | jq '.data.groups[].name'

# Alertmanager healthy
curl -s http://localhost:9093/-/ready

=============================================================

docker compose exec bot sh -lc 'printenv | egrep "USE_LIVE_EQUITY|DRY_RUN|BRIDGE_URL|PRODUCT_ID"'


=========================================================


# 1) sanity-check the env file really has the line
sudo tail -n +5 /opt/coinbase/env/bot.env

# 2) force-recreate the bot container so it re-reads the env file
docker compose up -d --force-recreate --no-deps bot

# 3) confirm inside the container
docker compose exec bot sh -lc 'printenv | egrep "USE_LIVE_EQUITY|DRY_RUN|BRIDGE_URL|PRODUCT_ID"'

===========================================================

git log --oneline --decorate --graph --all --max-count=15

# 1) switch to main and make it match origin/main exactly
git checkout main
git fetch origin
git reset --hard origin/main   # resets local main to the remote main

# 2) merge your feature branch into main
git merge --no-ff fix/live-equity

# 3) push updated main
git push origin main

# 4) delete the feature branch (optional)
git branch -D fix/live-equity
git push origin --delete fix/live-equity


===========================================================

# 1) See last logs for the bot (why it restarts)
docker compose logs --no-color --tail=200 bot

# 2) Reproduce build in a clean one-off container to get compile errors immediately
docker compose run --rm bot bash -lc 'go vet ./... && go build ./...'

==========================================================================================
# uploading Grafana DashBoard
curl -X POST -H "Content-Type: application/json" \
  -u admin:admin \
  http://localhost:3000/api/dashboards/db \
  -d @dashboard.json

===================================================================================

tree -L 2 ~/coinbase

Required inputs (no secrets)

✅ Confirm you’re OK to run the bot at 1s cadence (much noisier logs/CPU).

✅ Confirm you’re OK with aggressive sizing (example below uses 20% of equity per trade with 0.8% TP / 0.4% SL). If you want milder, say so and I’ll give a safer preset.

========================================================================================================

3) Aggressive sizing + TP-only behavior (no practical SL)

Important nuance: in your code, STOP_LOSS_PCT=0.00 makes stop equal to entry price, which can exit almost immediately on any dip. To effectively ignore stop-loss, set an absurdly large value so it never triggers.

Update these in /opt/coinbase/env/bot.env:

# ~20% of equity per trade (≈$19 on $96)
sudo sed -i 's/^RISK_PER_TRADE_PCT=.*/RISK_PER_TRADE_PCT=20.0/' /opt/coinbase/env/bot.env

# Take-profit at +0.8% (percentage units)
sudo sed -i 's/^TAKE_PROFIT_PCT=.*/TAKE_PROFIT_PCT=0.80/' /opt/coinbase/env/bot.env

# Effectively disable stop-loss by setting it impossibly wide
sudo sed -i 's/^STOP_LOSS_PCT=.*/STOP_LOSS_PCT=1000.0/' /opt/coinbase/env/bot.env

# Keep a daily breaker on (2%); adjust if you want more leash
sudo sed -i 's/^MAX_DAILY_LOSS_PCT=.*/MAX_DAILY_LOSS_PCT=2.0/' /opt/coinbase/env/bot.env

# Ensure long-only & ticks
sudo sed -i 's/^LONG_ONLY=.*/LONG_ONLY=true/' /opt/coinbase/env/bot.env
sudo grep -q "^USE_TICK_PRICE=" /opt/coinbase/env/bot.env || echo "USE_TICK_PRICE=true" | sudo tee -a /opt/coinbase/env/bot.env

# bounce the bot to load env
docker compose -f ~/coinbase/monitoring/docker-compose.yml up -d bot

============================================================================
# 
# 1) Show the active trading env toggles
grep -E '^(DRY_RUN|USE_LIVE_EQUITY|MAX_DAILY_LOSS_PCT|RISK_PER_TRADE_PCT|TAKE_PROFIT_PCT|STOP_LOSS_PCT|LONG_ONLY|USE_TICK_PRICE)=' /opt/coinbase/env/bot.env

# 2) Last 400 bot log lines around trade decisions/exits
docker compose -f ~/coinbase/monitoring/docker-compose.yml logs --tail=400 bot \
 | egrep -i 'LIVE ORDER|PAPER (BUY|SELL)|EXIT|CIRCUIT|EQUITY|step err|shutdown'

# 3) Current metrics sample (look for equity value the bot exported)
docker compose exec bot sh -lc 'curl -s http://localhost:8080/metrics | egrep -m5 "bot_equity_usd|bot_orders_total|bot_decisions_total" && echo'

=========================================================================

# Nice—trade counters are in. Here’s what I’d do next, in order:

# Restart & sanity-check

docker compose restart bot

# Verify metrics expose the new counter:
docker compose exec bot sh -lc 'curl -s http://localhost:8080/metrics | egrep -m5 "bot_trades_total|bot_decisions_total|bot_orders_total"'

# You should see:

bot_trades_total{result="open"}

bot_trades_total{result="win"}

bot_trades_total{result="loss"}

# Quick functional checks

# Open a tiny position (DryRun true if you want) and confirm open increments.

# Force a TP/SL on a small lot (tight TP/SL) and watch win or loss increment on exit.

Confirm bot_orders_total{mode="live|paper"} keeps matching actual orders.

Grafana panels (optional but recommended)

“Opens”: sum(increase(bot_trades_total{result="open"}[1h]))

“Wins/Losses”:

sum(increase(bot_trades_total{result="win"}[1h]))

sum(increase(bot_trades_total{result="loss"}[1h]))

Win rate:
sum(increase(bot_trades_total{result="win"}[1h])) / sum(increase(bot_trades_total{result=~"win|loss"}[1h]))

Alert ideas

No opens for N hours (stuck bot): sum(increase(bot_trades_total{result="open"}[6h])) == 0

Loss streak: increase(bot_trades_total{result="loss"}[1h]) > 5

Confirm strategy behavior matches your intent

While lots are open, discretionary SELL signals are ignored; only TP/SL closes will exit. Watch logs to ensure you see “HOLD” on SELLs during open lots and EXIT logs only on TP/SL triggers.

Extended model & thresholds sanity

If you’re using MODEL_MODE=extended, keep the debug logs on briefly and ensure features[n-1] lines are moving and Decision=... pUp=... looks reasonable.

Keep thresholds at standard (e.g., ~0.47/0.50) while validating pUp dynamics.

Pyramid rules validation

If pyramiding is enabled, test spacing/adverse-move gates and look for the [DEBUG] pyramid: blocks when adds are suppressed.

(Optional) Backtest/Replay

If you have a replay harness, run a short session to confirm opens/TP/SL exits produce the expected counts without touching live capital.

If you want, I can also add a tiny “win rate” gauge (computed) and a “last_trade_pl” gauge so you can watch outcomes at a glance.

=================================================================
BACKTEST
=================================================================
Switch to backtest (persistent)

Toggle the commands in docker-compose.yml:

sudo sed -i 's#^\(\s*\)command: /usr/local/go/bin/go run \. -live -interval .*#\1#command: /usr/local/go/bin/go run . -live -interval 15#' ~/coinbase/monitoring/docker-compose.yml
sudo sed -i 's#^\(\s*\)#command: /usr/local/go/bin/go run . -backtest /app/data/BTC-USD.csv -interval 1#;t; s#^\(\s*\)#command: /usr/local/go/bin/go run . -backtest /app/data/BTC-USD.csv -interval 1#' ~/coinbase/monitoring/docker-compose.yml


(That comments the live line if present and ensures the backtest line is active.)

Restart just the bot:

docker compose -f ~/coinbase/monitoring/docker-compose.yml up -d --force-recreate --no-deps bot

One-off backtest (no compose edit)
docker compose -f ~/coinbase/monitoring/docker-compose.yml run --rm bot \
  /usr/local/go/bin/go run . -backtest /app/data/BTC-USD.csv -interval 1

Make sure the CSV exists

Your compose mounts the repo at /app, so the backtester expects data/BTC-USD.csv in your repo:

ls -lh ~/coinbase/data/BTC-USD.csv


If you need a quick sample from the bridge (1-min candles), you can dump recent data (adjust limit as you like):

docker compose exec bot sh -lc '
curl -s "http://bridge:8787/candles?product_id=$PRODUCT_ID&granularity=ONE_MINUTE&limit=300" |
python - <<PY
import sys, json, csv
rows=json.load(sys.stdin)
w=csv.writer(sys.stdout)
w.writerow(["time","open","high","low","close","volume"])
for r in rows:
    w.writerow([r["start"],r["open"],r["high"],r["low"],r["close"],r["volume"]])
PY
' > ~/coinbase/data/BTC-USD.csv

Verify it’s backtesting

Logs should look like a backtest loop (no live order placements):

docker compose logs -f bot | head -n 50


Metrics should still emit decisions/orders, but orders will be paper (not “live”):

docker compose exec bot sh -lc 'curl -s http://localhost:8080/metrics | egrep -m10 "bot_decisions_total|bot_orders_total|bot_trades_total"'

(Optional) Env tweaks while backtesting

USE_TICK_PRICE is irrelevant in backtest; you can leave it as is.

DRY_RUN is ignored by the backtester (it doesn’t hit the live broker), so no change needed.

Want me to also wire a simple backtest report (final PnL, win/loss/open counts) to print at the end?

===============================================================

docker compose exec bot sh -lc 'curl -s http://localhost:8080/metrics | egrep -m10 "bot_decisions_total|bot_orders_total|bot_trades_total"'

==============================================================

watch -n 1 "docker compose exec -T bot sh -lc \"curl -sS http://localhost:8080/metrics | grep -E '^(bot_equity_usd|bot_decisions_total|bot_orders_total|bot_trades_total)' || true\""

================================================================================

✅ 1. Check from inside the running container

You can open a shell into the bot container and print its environment:

docker compose exec bot env | grep -E 'DRY_RUN|GRANULARITY|USE_TICK_PRICE|CANDLE_RESYNC_SEC|TICK_INTERVAL_SEC|WALK_FORWARD_MIN|TAKE_PROFIT_PCT|STOP_LOSS_PCT'


This will dump only the variables you care about.
If you want everything:

docker compose exec bot env

✅ 2. Inspect directly from Compose

If you suspect overrides from your docker-compose.yml or .env file, check what’s mounted:

cat monitoring/docker-compose.yml
cat /opt/coinbase/env/bot.env


Your bot likely loads from /opt/coinbase/env/bot.env (since I saw sed edits earlier).

✅ 3. Add debug logging inside the bot (optional)

If you want to log the parsed values as the bot sees them, you can sprinkle a one-liner in main.go or live.go after config load:

log.Printf("[CONFIG] USE_TICK_PRICE=%v TICK_INT ERVAL_SEC=%d CANDLE_RESYNC_SEC=%d WALK_FORWARD_MIN=%d TAKE_PROFIT_PCT=%.2f STOP_LOSS_PCT=%.2f",
    getEnvBool("USE_TICK_PRICE", false),
    getEnvInt("TICK_INTERVAL_SEC", 1),
    getEnvInt("CANDLE_RESYNC_SEC", 60),
    getEnvInt("WALK_FORWARD_MIN", 0),
    getEnvFloat("TAKE_PROFIT_PCT", 0.0),
    getEnvFloat("STOP_LOSS_PCT", 0.0))


That way every restart prints the effective values into your logs.

⚡ I’d recommend method 1 first (docker compose exec bot env | grep ...) to verify if what’s set inside the container matches your bot.env.

👉 Do you want me to add that [CONFIG] debug block permanently into main.go so every startup shows exactly what thresholds and tunables are being used?
================================
# backing up database or backup DB
sudo cp /opt/coinbase/state/bot_state.json /opt/coinbase/state/bot_state.json.bak.$(date +%F-%H%M)

# (optional) see your backups
ls -lt /opt/coinbase/state/bot_state.json.bak.*

# restore the newest backup over the live file
LATEST=$(ls -1t /opt/coinbase/state/bot_state.json.bak.* | head -n1)
sudo cp -a "$LATEST" /opt/coinbase/state/bot_state.json

==============================================================
Manual Trading

docker compose -f ~/coinbase/monitoring/docker-compose.yml exec bridge \
  sh -lc 'curl -s -X POST http://localhost:8787/order/market \
    -H "Content-Type: application/json" \
    -d '\''{"product_id":"BTC-USD","side":"BUY","quote_size":"5.00"}'\'' ' \
  | tee order_response.json | jq .

====================================
chidi@Dynamo:~/coinbase/monitoring$ docker compose -f ~/coinbase/monitoring/docker-compose.yml exec bridge \
  sh -lc 'curl -s -X POST http://localhost:8787/order/market \
    -H "Content-Type: application/json" \
    -d '\''{"product_id":"BTC-USD","side":"BUY","quote_size":"5.00"}'\'' ' | tee order.json | jq .

OID=$(jq -r '.order_id // .success_response.order_id' order.json)
docker compose -f ~/coinbase/monitoring/docker-compose.yml exec bridge \
  curl -s http://localhost:8787/order/$OID | jq .
{
  "success": true,
  "success_response": {
    "order_id": "34b0bfa5-daab-463f-b9da-074af867af34",
    "product_id": "BTC-USD",
    "side": "BUY",
    "client_order_id": "cd029765f32144949be501bb2b2bac4d",
    "attached_order_id": ""
  },
  "order_configuration": {
    "market_market_ioc": {
      "quote_size": "5.00",
      "rfq_enabled": false,
      "rfq_disabled": false,
      "reduce_only": false
    }
  }
}
{
  "order_id": "34b0bfa5-daab-463f-b9da-074af867af34",
  "status": "UNKNOWN",
  "filled_size": "4.9596880743",
  "average_filled_price": "108932.31"
}
chidi@Dynamo:~/coinbase/monitoring$ 
============================================================
docker compose -f ~/coinbase/monitoring/docker-compose.yml logs -f --tail=200 bot \
  | egrep "LIVE ORDER|\[WARN\] partial fill"
  
============================================================

What to expect in behavior

First open lot = the runner. It uses your normal SL, a stretched TP (internally doubled), and now a trailing stop:

When price ≥ OpenPrice * (1 + TRAIL_ACTIVATE_PCT/100), trailing turns on.

The trail’s stop follows the best price by TRAIL_DISTANCE_PCT.

If price falls back to the trailing stop, the runner exits via the normal market-close path.

Later adds (while open) are scalps; unchanged TP/SL logic (no trailing on them).

If the runner exits first, the newest remaining lot is auto-promoted to runner; its trailing fields are reset and will activate once thresholds are met.

Adds are still gated by spacing + adverse move + the independent anchor and the small runner-gap guard you asked for.

==============================================================
Here’s what that debug line is telling you, field by field, and why the add was blocked:

[DEBUG] pyramid: blocked by anchor/last gates; 
price=114742.12 
combined_gate<=113224.11 
last_gate=113224.11 
anchor_gate=110823.58 
pct=1.500

What each number means

price=114742.12
The current market price (last close/tick the bot is using).

pct=1.500
Your PYRAMID_MIN_ADVERSE_PCT is 1.5%. Adds are only allowed after at least a 1.5% pullback from the gate(s).

last_gate=113224.11
This is computed from the most recent entry price (lastEntry) pushed down by 1.5%:
last_gate = lastEntry × (1 - 0.015)
So lastEntry ≈ 113224.11 / 0.985 ≈ 114.95k.

anchor_gate=110823.58
This is computed from your independent pyramiding anchor (pyramidAnchorPrice) pushed down by 1.5%:
anchor_gate = anchor × (1 - 0.015)
So anchor ≈ 110823.58 / 0.985 ≈ ~112.5k.
(Anchor only ratchets in the adverse direction for a BUY—i.e., it can move lower, but not higher.)

combined_gate<=113224.11
We require price ≤ max(last_gate, anchor_gate) for a BUY.
Since max(113224.11, 110823.58) = 113224.11, the combined gate is 113,224.11.

Why the add was blocked

Because the current price (114,742.12) is above the combined gate (113,224.11), it hasn’t pulled back enough.
Put differently, from your latest entry (~114.95k), price has only dipped to 114.74k (≈0.18% pullback), but you require ≥1.5%. So no add yet.

(The log message is specifically the anchor/last-gate check. The spacing guard and runner-gap guard are additional checks that could also block, but they aren’t what triggered here.)

Quick formulas (BUY)

last_gate = lastEntry × (1 - PYRAMID_MIN_ADVERSE_PCT/100)

anchor_gate = anchor × (1 - PYRAMID_MIN_ADVERSE_PCT/100)

combined_gate = max(last_gate, anchor_gate)

Allow add only if price ≤ combined_gate.

How to “tune” this behavior

Allow adds sooner: lower PYRAMID_MIN_ADVERSE_PCT (e.g., from 1.5 → 0.8 or 0.5).
That raises both gates and makes combined_gate easier to reach.

Let the anchor help more: the anchor only ratchets down (for BUY). After a bigger dip, it’ll sit lower and can become the controlling gate during later rallies. If you want the anchor to be more permissive, you could reset it when flat (already happens), or set it manually by script when you truly want a new baseline.

Keep adds “not too close” to the runner: the runner-gap guard is separate, but keep in mind loosening adverse% too much could cause adds to cluster near the runner.

If you paste another similar line later, you can decode it the same way: compute the implied lastEntry and anchor from their gates by dividing by (1 - pct/100), then check how far the real-time price is from the combined_gate.

You said:
what is add?
ChatGPT said:

An add = a pyramiding add-on entry.

In this bot, it means: while you already have an open position (the “runner” and possibly some scalps), the bot opens another small lot in the same direction to scale into strength/volatility—but only if strict gates are satisfied.

Key points:

Not the first entry—it’s an additional lot placed after you’re already long (or short).

Rule-gated to avoid chasing:

Spacing gate: must wait ≥ PYRAMID_MIN_SECONDS_BETWEEN since the last add.

Adverse-move gate: price must have pulled back at least PYRAMID_MIN_ADVERSE_PCT from a gate price.

Independent anchor gate: uses a stable pyramidAnchorPrice so adds don’t “jitter” off the newest scalp.

Last-entry gate: respects the most recent entry too.

Runner gap guard: don’t stack adds too close to the runner’s entry.

Lot cap: never exceed the max concurrent lots (default 3).

Sizing: still determined by your RISK_PER_TRADE_PCT (bounded by ORDER_MIN_USD), so adds increase exposure in controlled increments.

Behavior: the runner stays longer (with trailing logic); adds are short-lived scalps with fixed TP/SL.

So when you see logs like “pyramid: blocked by …” that’s the bot deciding not to place an add yet because one of those gates wasn’t met.
===================================================
With your current code, the runner doesn’t exit at the base TAKE_PROFIT_PCT (e.g., 1.5%). It uses that number only to derive a farther static TP:

Runner TP: runnerTP = 2 × TAKE_PROFIT_PCT (so ~3.0% if TP=1.5%)

Runner SL: same as normal (STOP_LOSS_PCT)

Runner trailing: arms when price reaches TRAIL_ACTIVATE_PCT (your env knob), then exits on a pullback of TRAIL_DISTANCE_PCT

Meanwhile, scalp lots still use the base TAKE_PROFIT_PCT (e.g., 1.5%) as their fixed take-profit.

So the runner can exit by:

trailing (once armed), 2) its stretched TP (~2× TP), or 3) stop-loss—whichever happens first.
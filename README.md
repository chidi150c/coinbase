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
export BRIDGE_URL="http://127.0.0.1:8787"
go run verify.go


Expected:

GET /health → 200 with {"ok": true}

GET /accounts?limit=1 → 200 with an accounts object

GET /product/BTC-USD → 200 with product/ticker snapshot

Final line: ✅ Bridge reachable; credentials verified via /accounts.

Environment & best practices

TL;DR

Prod: Don’t parse .env in the app. Inject env vars via the runtime (systemd EnvironmentFile, Docker env_file, K8s Secrets). Keep secrets in a vault/CI store.

Dev: Load .env outside the app (e.g., direnv, set -a; source .env) or let the Python bridge load .env for its own use.

Code: Read from os.Getenv, validate, fail fast. Never log secrets.

Production (Linux)

systemd: EnvironmentFile=/opt/coinbot/coinbot.env (0600, owned by service user).

Docker/Compose: env_file: ./prod.env (not in git) or K8s secrets.

Separate keys per env; no withdrawals permission; rotate regularly.

Time sync: sudo timedatectl set-ntp true.

Local dev

direnv, or make run that does:

set -a; source .env; set +a; go run .


The bridge already loads ~/coinbase/.env itself for Coinbase.

IP allowlist

curl -s https://ifconfig.me
# Add this IP to the Coinbase key allowlist (if you enable allowlisting).

Bridge API

Bound to 127.0.0.1. Do not expose externally.

GET /health → {"ok": true} if bridge is up.

GET /accounts?limit=<n> → Authenticated account list (sanity check for creds, IP allowlist, clock).

GET /product/{product_id} → Product snapshot/ticker (e.g., BTC-USD).

POST /orders/market_buy → Places a live market buy using quote size (USD).

{
  "client_order_id": "your-uuid-or-string",
  "product_id": "BTC-USD",
  "quote_size": "5.00"
}


⚠️ The order endpoint is live. Test with tiny amounts only.

Troubleshooting

verify.go can’t reach bridge

Ensure uvicorn is running on 127.0.0.1:8787.

Set BRIDGE_URL appropriately.

/accounts 401/403

Wrong credential style (PEM vs legacy secret).

IP allowlist blocking current IP.

System time skew (enable NTP).

PEM parse errors

Keep full PEM with BEGIN/END.

If stored on one line, include literal \n; the bridge normalizes it.

Corporate proxy causes 401 on public endpoints

Keep all calls local via the bridge; ensure the bridge can reach Coinbase outbound.

Security checklist

 Keys are read-only during development.

 Withdrawals disabled on API key.

 Bridge bound to 127.0.0.1 only.

 .env not committed; permissions 0600.

 Separate keys per env; rotate at least every 90 days.

 Alerts/logs don’t print secrets.

Next steps

Add a /candles?product_id=BTC-USD&granularity=ONE_MINUTE&limit=300 endpoint to the bridge so the Go bot can warm up with real OHLCV.

Add Prometheus /metrics to the Go bot and a small Grafana dashboard.

Wire a Slack/webhook for circuit-breaker trips and order events.

Dockerize the bridge and run it as a systemd service or Compose stack (still on localhost network only).

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


======================================================================================
AI Prompt
======================================================================================

Continue the AI-Assisted Coinbase Trading Bot (Go + FastAPI)

You are inheriting a working project. Read everything below carefully and preserve all existing behaviors and interfaces while progressing to the next phases.

Aim (single sentence)

Build a safe, monitored, AI-assisted spot-trading bot for Coinbase Advanced Trade, with a Go trading engine (strategy, risk, metrics) that talks only to a small Python FastAPI sidecar wrapping the official coinbase.rest.RESTClient.

What exists today (must remain stable)
Architecture

Go bot (package main, single folder; multiple files): strategy + tiny ML model, risk mgmt, position mgmt, Prometheus metrics, /healthz.

Python FastAPI sidecar (app.py): fronts Coinbase Advanced via coinbase.rest.RESTClient; Go bot calls it over HTTP (localhost).

Sidecar API (keep these exact routes/behavior)

GET /health → {"ok": true}

GET /accounts?limit=<int>

GET /product/{product_id}

GET /candles?product_id=BTC-USD&granularity=ONE_MINUTE&limit=300
Returns normalized OHLCV array:
[{"start": "<unix|RFC3339>", "open":"", "high":"", "low":"", "close":"", "volume":""}, ...]

POST /orders/market_buy (legacy BUY only; keep working)

POST /order/market (unified BUY/SELL; body: {"product_id":"BTC-USD","side":"BUY|SELL","quote_size":"5.00","client_order_id"?})
Returns normalized order info (strings): {"order_id","avg_price","filled_base","quote_spent"}

Go bot interfaces & ops

Flags: -live, -backtest <csv>, -interval <seconds>

HTTP (bot): /healthz → ok, /metrics → Prometheus text

Metrics:

bot_orders_total{mode="paper|live",side="BUY|SELL"}

bot_decisions_total{signal="buy|sell|flat"}

bot_equity_usd

Risk defaults (safety first): DRY_RUN=true, LONG_ONLY=true, ORDER_MIN_USD=5.00, MAX_DAILY_LOSS_PCT=1.0

Model/strategy: tiny logistic-like pUp + optional MA(10)/MA(30) filter; thresholds tunable via env.

Environment handling (no shell exports)

Go bot auto-loads .env (dependency-free loader) and only reads the keys it needs.

Sidecar loads full .env (including multiline PEM) and normalizes \n in the private key.

Important: BRIDGE_URL must be a clean URL (no trailing inline comments). Code already trims comments/whitespace defensively.

Key .env knobs (current)
# Trading
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

# Strategy thresholds
BUY_THRESHOLD=0.55
SELL_THRESHOLD=0.45
USE_MA_FILTER=true

# Sidecar / Coinbase
COINBASE_API_KEY_NAME=organizations/.../apiKeys/...
COINBASE_API_PRIVATE_KEY=-----BEGIN PRIVATE KEY-----\n...PEM...\n-----END PRIVATE KEY-----

Repository layout (Option-A single package, do not change paths now)
app.py                  # FastAPI sidecar (Python)
verify.go               # verifies bridge/creds via sidecar

env.go                  # .env loader + env helpers + threshold init
config.go               # Config struct + loader from env
indicators.go           # SMA, RSI, ZScore
model.go                # tiny micro-model (logistic-like)
strategy.go             # Candle, Signal, Decision, decide()
broker.go               # Broker interface + types
broker_bridge.go        # HTTP client to sidecar (hardened URL & request creation)
broker_paper.go         # in-memory paper broker
trader.go               # position/risk + synchronized step() + live exits
metrics.go              # Prometheus collectors
backtest.go             # CSV loader + backtest runner
live.go                 # real-time loop (pulls real candles)
main.go                 # flags, wiring, HTTP server

Current status (“where we are”)

Sidecar endpoints implemented & tested (including /candles and /order/market).

Go bot runs in paper mode with real candles, emits PAPER BUY/SELL, HOLD, EXIT, and metrics.

Logs show trades like:
PAPER BUY quote=5.00 base=... stop=... take=... [pUp=..., ma10=... vs ma30=...]

Verified /metrics, /healthz, and sidecar /accounts, /product, /candles.

URL parse issues from inline comments fixed (hardened loader + broker).

Remaining: systemd unit, Prometheus scrape/dashboards, run/record backtests, optional alerts/retries.

Phases (in order)

Phase 0 — Ground rules & accounts ✅

Phase 0.7 — Verify credentials (read-only via sidecar) ✅

Phase 1 — Sidecar scaffold ✅

Phase 1.1 — Sidecar integrations (/candles, /order/market) ✅

Phase 2 — Go bot bootstrap (env, flags, metrics server) ✅

Phase 2.1 — Brokers (bridge + paper) ✅

Phase 3 — Indicators & micro-model + thresholds via .env ✅

Phase 4 — Trader & risk: stops/takes, long-only, daily breaker ✅

Phase 5 — Live loop with real candles ✅

Phase 6 — Ops & observability (service, dashboards) ⏳

Phase 7 — Backtest & paper reporting ⏳

Phase 8 — Guarded go-live (DRY_RUN=false with safeguards) ⏳

Phase 9 — Hardening (retries, alerts, tests) ⏳

What to do next (your tasks)

Short-term (Phase 6/7/8):

Serviceization (optional but preferred):

Provide a systemd unit (/etc/systemd/system/coinbot.service) that runs go run . -live -interval 15 in /home/chidi/coinbase, restarts on failure. Include commands to install/enable and how to tail logs with journalctl.

Prometheus & Grafana setup artifacts:

Prometheus scrape job snippet for ${PORT}/metrics.

A basic Grafana JSON dashboard: orders (live/paper), decisions by signal, equity gauge, and a panel for recent log messages if using Loki (include Loki panel conditionally).

Backtest run + summary:

Accept a CSV path, run go run . -backtest <file>, and emit a concise report (wins, losses, net P/L change vs. USD_EQUITY), saving results to a timestamped JSON/CSV in ./runs/.

Guarded live smoke test plan:

Document the change to DRY_RUN=false, keep LONG_ONLY=true, ORDER_MIN_USD=5, MAX_DAILY_LOSS_PCT=1.

Provide a checklist to verify one successful small order via /order/market and confirm in metrics and logs.

Medium-term (Phase 9):

Add idempotent retry/backoff for sidecar HTTP calls (5xx, timeouts).

Structured logs (JSON) with fields: ts, level, msg, decision, pUp, price, side, quote, base, stop, take.

Optional Slack alerts (webhook URL env var) on EXIT and on daily breaker trips.

Unit tests for indicators.go, decide(), and env loading edge cases.

Invariants / Constraints (do not break)

Do not change or remove existing sidecar routes or their payload shapes.

Do not rename/move Go files or packages (single-package layout stays).

Do not change default safety settings; keep paper mode by default.

Keep /metrics names and label sets exactly as defined.

Ensure backwards compatibility for any new CLI flags or env keys (additive only).

Deliverables

Concrete code changes (copy-pasteable) for each task above.

Updated README sections for serviceization, metrics scraping, dashboard import, and backtest usage.

A short validation checklist with exact shell commands to prove success.

Useful commands (must stay working)
# Sidecar
uvicorn app:app --host 127.0.0.1 --port 8787 --reload

# Verify credentials through sidecar
go run verify.go

# Bot (paper)
go run . -live -interval 15
curl -s localhost:${PORT}/metrics | head

# Backtest
go run . -backtest path/to/candles.csv


Proceed now with Phase 6/7/8 tasks, preserving all invariants. Produce code snippets, config files, and README updates.



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
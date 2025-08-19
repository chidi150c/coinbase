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
ssh-keygen -t ed25519 -C "chidi150c@gmail.com"
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

Happy building!






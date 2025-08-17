# coinbase
AI-assisted crypto trading bot in Go (Coinbase, paper/live, risk-guarded)

Awesome — let’s build this the right way: safe first, observable, then (optionally) live. Below is an end-to-end, phase-by-phase guide with checklists, shell commands, and copy-paste Go code you can run locally. It uses a strict “paper/dry-run → guarded live” path, circuit breakers, and basic monitoring. The exchange integration targets Coinbase Advanced Trade API (the successor to Coinbase Pro). You’ll start completely offline (CSV backtests), then progress to read-only API, then to tiny-size live orders.

WSL or Powershell?
Use WSL2 if you can—it gives you a Linux-like environment that matches most production servers and makes ops (Prometheus, curl, systemd-like flows) smoother. PowerShell is fine if you prefer native Windows and just want to run the bot locally without extra Linux tooling.

Here’s a quick decision guide:

Pick WSL2 if…

You’ll eventually deploy on Linux (cloud/VPS/VM).

You want Linux-y workflows: bash, grep, curl, journalctl-style logs, easier Prometheus/Grafana setup.

You like using .env files and shell exports exactly as shown.

Pick PowerShell if…

You want zero extra install and to stay 100% Windows-native.

You’ll run the bot as a Windows Service / Task Scheduler and don’t need Linux utilities.

You’re comfortable setting env vars in PowerShell.

Both work for the Go bot. My default recommendation for this project: WSL2 (closest parity to a future Linux server, fewer surprises).

Quick setup cheatsheets
Option A — WSL2 (recommended)

Install WSL & Ubuntu:

wsl --install -d Ubuntu


Inside Ubuntu:

sudo apt-get update
sudo apt-get install -y golang git make curl chrony
go version


Project prep:

mkdir -p ~/coinbot && cd ~/coinbot
go mod init example.com/coinbot
# create .env, paste variables
export $(grep -v '^#' .env | xargs)


Run:

go run . -backtest data/BTC-USD.csv
# or live paper:
go run . -live
curl -s localhost:8080/healthz


Ops ideas: screen/tmux, or (if your WSL enables it) systemd units; easy Prometheus scraping from Windows host at http://localhost:8080/metrics.

Option B — PowerShell (Windows native)

Install Go from https://go.dev/dl and verify:

go version


Project prep:

mkdir $HOME\coinbot; cd $HOME\coinbot
go mod init example.com/coinbot
# create .env and fill it, then:
Get-Content .env | ForEach-Object {
  if ($_ -match '^\s*#') { return }
  $parts = $_ -split '=',2
  if ($parts.Length -eq 2) { [Environment]::SetEnvironmentVariable($parts[0], $parts[1], "Process") }
}


Run:

go run . -backtest data\BTC-USD.csv
go run . -live
Invoke-RestMethod http://localhost:8080/healthz


Ops ideas: run via Task Scheduler or wrap with NSSM to make a Windows Service; logs go to console or a file you rotate.

Small gotchas to be aware of

IP allowlist on Coinbase: Whether you use PowerShell or WSL, your outbound public IP is the same (your machine/ISP or VPN). If you allowlist, add that public IP.

Time sync: WSL2 uses Windows’ clock (usually fine). On native Linux you might install chrony; on Windows you don’t need it.

File paths: Use Linux-style paths in WSL (~/coinbot) and Windows paths in PowerShell ($HOME\coinbot).

Services: Windows uses Task Scheduler/Services; Linux/WSL can use screen/tmux or (when available) systemd.
# Deployment Runbook — Coinbase Bot + Bridge on Linode

## One-time server prep (Ubuntu)
```bash
sudo apt-get update && sudo apt-get install -y docker.io docker-compose-plugin
sudo usermod -aG docker $USER
mkdir -p /opt/coinbase/env /opt/coinbase/state /opt/coinbase/monitoring
sudo chown -R $USER:$USER /opt/coinbase

# Place these files (already confirmed):
# /opt/coinbase/monitoring/docker-compose.yml
# /opt/coinbase/monitoring/prometheus/prometheus.yml
# /opt/coinbase/env/bot.env
# /opt/coinbase/env/bridge.env

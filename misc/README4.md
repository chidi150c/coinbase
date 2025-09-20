# From ~/coinbase/monitoring
docker compose config | sed -n '/bridge_binance:/,/^[^ ]/p'

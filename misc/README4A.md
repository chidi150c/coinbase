 docker logs monitoring-bot_binance-1 --since 10m 2>&1 | grep "TRACE dataset"
2026/05/20 15:28:57 TRACE dataset.rows total=6000 labeled=305 up=142 down=163 skipped=5659 bad=0 horizon=15 edge=0.003000 fee_pct=0.1000 min_edge_pct=0.1000
chidi@localhost:~/coinbase/monitoring$ ^C
chidi@localhost:~/coinbase/monitoring$




chidi@localhost:~/coinbase/monitoring$ ls
alertmanager  docker-compose.override.yml  docker-compose.prod.yml  docker-compose.yml  grafana  nohup.out  order.json  order_response.json  prometheus
chidi@localhost:~/coinbase/monitoring$



Stop only the monitoring containers and leave trading alive.

Run:

docker stop monitoring-grafana-1 \
monitoring-prometheus-1 \
monitoring-alertmanager-1 \
monitoring-bot-1 \
monitoring-bridge-1

Then verify:

docker ps
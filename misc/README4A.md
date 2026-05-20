 docker logs monitoring-bot_binance-1 --since 10m 2>&1 | grep "TRACE dataset"
2026/05/20 15:28:57 TRACE dataset.rows total=6000 labeled=305 up=142 down=163 skipped=5659 bad=0 horizon=15 edge=0.003000 fee_pct=0.1000 min_edge_pct=0.1000
chidi@localhost:~/coinbase/monitoring$ ^C
chidi@localhost:~/coinbase/monitoring$

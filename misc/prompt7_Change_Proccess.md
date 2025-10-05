Generate a full copy of {{
services:
  bridge_hitbtc:
    image: ghcr.io/${GHCR_NS:-chidi150c}/coinbase-bridge-hitbtc:${IMAGE_SHA}  # pinned to CI SHA
    container_name: bridge_hitbtc
    env_file:
      - /opt/coinbase/env/hitbtc.env   # includes PORT, STALE_MS, optional SYMBOL
    ports:
      - "8788:8788"
    networks:
      - monitoring_network
    restart: unless-stopped

  bridge_binance:
    image: ghcr.io/${GHCR_NS:-chidi150c}/coinbase-bridge-binance:${IMAGE_SHA} # pinned to CI SHA
    container_name: bridge_binance
    env_file:
      - /opt/coinbase/env/binance.env  # includes PORT, STALE_MS, optional SYMBOL
    ports:
      - "8789:8789"
    networks:
      - monitoring_network
    restart: unless-stopped

  bridge:
    image: ghcr.io/${GHCR_NS:-chidi150c}/coinbase-bridge:latest
    env_file:
      - /opt/coinbase/env/bridge.env
    expose:
      - "8787"
    restart: unless-stopped
    healthcheck:
      test: ["NONE"]   # image may not include wget/curl
    logging:
      driver: "json-file"
      options:
        max-size: "10m"
        max-file: "5"
    ports:
      - "8787:8787"
    networks:
      monitoring_network:
        aliases: [bridge]

  bot_hitbtc:
    depends_on: [bridge_hitbtc]
    image: ghcr.io/${GHCR_NS:-chidi150c}/coinbase-bot:${IMAGE_SHA}
    command: [ "sh", "-lc", "stdbuf -oL -eL ./bot -live -interval 1 2>&1 | tee -a /opt/coinbase/logs/bot.log" ]
    volumes:
      - /opt/coinbase/env:/opt/coinbase/env:ro
      - /opt/coinbase/state:/opt/coinbase/state
    env_file:
      - /opt/coinbase/env/bot_hitbtc.env      # broker/product/state overrides for HitBTC
    restart: unless-stopped
    expose:
      - "8080"
    healthcheck:
      test: ["NONE"]
    logging:
      driver: "json-file"
      options:
        max-size: "10m"
        max-file: "5"
    networks:
      monitoring_network:
        aliases: [bot-hitbtc]

  # NEW: second bot instance for Binance
  bot_binance:
    depends_on: [bridge_binance]
    image: ghcr.io/${GHCR_NS:-chidi150c}/coinbase-bot:${IMAGE_SHA}
    command: [ "sh", "-lc", "stdbuf -oL -eL ./bot -live -interval 1 2>&1 | tee -a /opt/coinbase/logs/bot.log" ]
    volumes:
      - /opt/coinbase/env:/opt/coinbase/env:ro
      - /opt/coinbase/state:/opt/coinbase/state
    env_file:
      - /opt/coinbase/env/bot_binance.env     # broker/product/state overrides for Binance
    restart: unless-stopped
    expose:
      - "8080"
    healthcheck:
      test: ["NONE"]
    logging:
      driver: "json-file"
      options:
        max-size: "10m"
        max-file: "5"
    networks:
      monitoring_network:
        aliases: [bot-binance]

  bot:
    image: ghcr.io/${GHCR_NS:-chidi150c}/coinbase-bot:${IMAGE_SHA}
    command: [ "sh", "-lc", "stdbuf -oL -eL ./bot -live -interval 1 2>&1 | tee -a /opt/coinbase/logs/bot.log" ]
    volumes:
      - /opt/coinbase/env:/opt/coinbase/env:ro
      - /opt/coinbase/state:/opt/coinbase/state
    env_file:
      - /opt/coinbase/env/bot_coinbase.env    # <-- NEW: pin Coinbase broker/symbol/state
    restart: unless-stopped
    expose:
      - "8080"
    healthcheck:
      test: ["NONE"]   # distroless image: no shell/curl inside
    logging:
      driver: "json-file"
      options:
        max-size: "10m"
        max-file: "5"
    networks:
      monitoring_network:
        aliases: [bot, coinbase-bot]

  prometheus:
    image: prom/prometheus:latest
    ports: ["9090:9090"]
    volumes:
      - ./prometheus/prometheus.yml:/etc/prometheus/prometheus.yml
      - monitoring_prometheus_data:/prometheus
    command:
      - --config.file=/etc/prometheus/prometheus.yml
      - --storage.tsdb.path=/prometheus
      - --storage.tsdb.retention.time=15d
      - --storage.tsdb.retention.size=2GB
    restart: unless-stopped
    networks: [monitoring_network]

  alertmanager:
    image: prom/alertmanager:latest
    ports: ["9093:9093"]
    volumes:
      - ./alertmanager/alertmanager.yml:/etc/alertmanager/alertmanager.yml:ro
    restart: unless-stopped
    networks: [monitoring_network]

  grafana:
    image: grafana/grafana:latest
    ports: ["3000:3000"]
    environment:
      GF_SECURITY_ADMIN_USER: admin
      GF_SECURITY_ADMIN_PASSWORD: admin
    volumes:
      - monitoring_grafana_data:/var/lib/grafana
    depends_on: [prometheus, bot, bridge]
    networks: [monitoring_network]

volumes:
  monitoring_prometheus_data:
  monitoring_grafana_data:
  coinbase_state:

networks:
  monitoring_network:
    driver: bridge

}} with only the necessary minimal changes to implement {{update bot, bot_binance, and bot_hitbtc service commands in docker-compose.yml to sh -lc "stdbuf -oL -eL ./bot 2>&1 | tee -a /opt/coinbase/logs/bot.log"}}. Do not alter any function names, struct names, metric names, environment keys, log strings, or the return value of identity functions (e.g., Name()). Keep all public behavior, identifiers, and monitoring outputs identical to the current baseline. Only apply the minimal edits required to implement {{update bot, bot_binance, and bot_hitbtc service commands in docker-compose.yml to sh -lc "stdbuf -oL -eL ./bot 2>&1 | tee -a /opt/coinbase/logs/bot.log"}}. Return the complete file, copy-paste ready, in IDE.

Distill the Merged Specification (A+B) into a single invariant project spec that is core-trading focused.
Follow these rules exactly:

Format & Length

Output must be a sectioned bullet-point document (no paragraphs).

Use this section order and headings exactly:

Paths & Key Files

Bot Runtime (Go, :8080)

Strategy & Model

Configuration (env keys)

Bridge Service (:8787)

Monitoring Stack (Compose/Prometheus/Grafana)

Backtest

Minimal Ops Commands (VM)

CI/CD (Appendix)

≤ 50% of the merged spec length; if uncertain, cap at ≤ 6,000 characters total.

Content Requirements (no prose, no redundancy)

Keep every critical identifier: file paths, env var names, ports, metric names, HTTP endpoints, runtime processes/behaviors.

Do NOT invent new keys/files; only include items present in the merged spec.

Prioritize the trading system (bot/strategy/broker/bridge/metrics/runtime).

Explicitly include:

VM runtime dirs & mounts: /opt/coinbase/env/, /opt/coinbase/state/, and STATE_FILE=/opt/coinbase/state/bot_state.json.

Bot HTTP surfaces: /healthz, /metrics; Bridge: /health, /accounts, /product/{id}, /candles, /price, /orders/market_buy, /order/market.

All metrics names used (e.g., bot_decisions_total{...}, bot_orders_total{...}, bot_trades_total{...}, bot_equity_usd, bot_model_mode{...}, bot_vol_risk_factor, bot_walk_forward_fits_total).

All env keys in the merged spec (risk/sizing, pyramiding, trailing, strategy thresholds, ops, WS toggles, fees/state).

Insufficient-funds fallback behavior (the exact WARN log line) for live open orders.

Prometheus job names/targets (prometheus → localhost:9090, coinbase-bot → bot:8080) and retention flag --storage.tsdb.retention.size=2GB.

Minimal ops commands with where they run (VM): rolling the stack, in-network health checks, and image/ps verification.

Eliminate explanations; keep bullets terse but do not drop identifiers.

If space is tight

Compress wording first; never drop identifiers (paths/env/ports/metrics/endpoints).

Omit details before omitting any core-trading details.

Produce the final document now.
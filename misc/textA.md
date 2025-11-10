# === Broker wiring (Binance direct) ===
BROKER=binance
PRODUCT_ID=BTCUSDT
GRANULARITY=ONE_MINUTE
STATE_FILE=/opt/coinbase/state/bot_state.newbinance.json
BOT_ENV_PATH=/opt/coinbase/env/bot_binance.env

# === Execution cadence (tick loop + periodic candle resync) ===
BRIDGE_URL=http://bridge_binance:8789
USE_TICK_PRICE=true
TICK_INTERVAL_SEC=1
CANDLE_RESYNC_SEC=60

# === Risk & sizing ===
DRY_RUN=false
MAX_DAILY_LOSS_PCT=2.0
USD_EQUITY=250.0
MAX_HISTORY_CANDLES=6000
RISK_PER_TRADE_USD=80.0

# === Position controls ===
ALLOW_PYRAMIDING=true
PYRAMID_DECAY_LAMBDA=0.02
PYRAMID_MIN_SECONDS_BETWEEN=0
PYRAMID_MIN_ADVERSE_PCT=1.5
PYRAMID_DECAY_MIN_PCT=0.4
MAX_CONCURRENT_LOTS=8

# --- Optional: per-add TP decay for scalp lots (legacy; kept for logs/estimates) ---
SCALP_TP_DECAY_ENABLE=true
SCALP_TP_DEC_MODE=exp
SCALP_TP_DEC_PCT=0.20
SCALP_TP_DECAY_FACTOR=0.9850
SCALP_TP_MIN_PCT=0.6

# === Exits (USD profit-gate + USD trailing) ===
# Exits will not arm/trigger until per-lot NET PnL ≥ PROFIT_GATE_USD
PROFIT_GATE_USD=0.1

# Trailing activation thresholds (USD, per lot NET PnL)
TRAIL_ACTIVATE_USD_RUNNER=0
TRAIL_ACTIVATE_USD_SCALP=0

# Trailing distances (percent, post-activation)
TRAIL_DISTANCE_PCT_RUNNER=0.40
TRAIL_DISTANCE_PCT_SCALP=0.25

# Optional: maker-friendly offset (bps) for fixed-TP scalps (>4th lot on a side)
TP_MAKER_OFFSET_BPS=1

# === Fees & exchange minimums ===
FEE_RATE_PCT=0.10
ORDER_MIN_USD=10

# === Strategy thresholds ===
BUY_THRESHOLD=0.52
SELL_THRESHOLD=0.47
USE_MA_FILTER=true
BACKTEST_SLEEP_MS=100

# === Ops & model toggles ===
PORT=8080
MODEL_MODE=extended
WALK_FORWARD_MIN=1
VOL_RISK_ADJUST=true
DAILY_BREAKER_MARK_TO_MARKET=true

# === Entry/shorting constraints ===
LONG_ONLY=false
REQUIRE_BASE_FOR_SHORT=true

# === Paper/backtest seed (ignored in live if broker returns balances) ===
PAPER_BASE_BALANCE=0.05

# === Optional overrides (mainly for backtests / guards) ===
BASE_ASSET=BTC
BASE_STEP=0.000001
PRICE_TICK=0.1         # price increment (tick)
QUOTE_STEP=0.01         # quote asset step (USDT/USD cent precision)
MIN_NOTIONAL=10.0       # minimum notional in quote (typical Binance spot)

# === Live equity: broker provides balances ===
USE_LIVE_EQUITY=true

# --- Risk ramping (per-side; you already enabled it) ---
RAMP_ENABLE=true
RAMP_MODE=linear
RAMP_START_PCT=8.0
RAMP_STEP_PCT=1.0
RAMP_MAX_PCT=12.0
# For exp mode:
# RAMP_GROWTH=1.25
# RAMP_MAX_PCT=6.0

# === Legacy exit knobs (kept for back-compat/logging only; USD gate governs arming) ===
TAKE_PROFIT_PCT=1.9
STOP_LOSS_PCT=1000.00
TRAIL_ACTIVATE_PCT=1.9
TRAIL_DISTANCE_PCT=0.4

FORCE_FILTERS_REMOTE=1
# in the container env or compose file
LOG_LEVEL=TRACE

# === Order entry / maker placement ===
ORDER_TYPE=limit
SPREAD_MIN_BPS=0
LIMIT_PRICE_OFFSET_BPS=0.01
LIMIT_TIMEOUT_SEC=900

# =========== Reprice (maker-chase) ================
REPRICE_ENABLE=true
REPRICE_INTERVAL_MS=1000
REPRICE_MIN_IMPROV_TICKS=0
REPRICE_MIN_EDGE_USD=0      # <<< lowered so tiny orders can reprice
REPRICE_MAX_DRIFT_BPS=1.0
REPRICE_MAX_COUNT=0

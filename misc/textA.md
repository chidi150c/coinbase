# === Broker wiring (Coinbase via the bridge) ===
# This mirrors your coinbase example: the bot talks to the Coinbase bridge for price/equity.
BROKER=bridge
PRODUCT_ID=BTC-USD
GRANULARITY=ONE_MINUTE
STATE_FILE=/opt/coinbase/state/bot_state.newcoinbase.json

# === Execution cadence ===
BRIDGE_URL=http://bridge:8787
USE_TICK_PRICE=true
TICK_INTERVAL_SEC=1
CANDLE_RESYNC_SEC=60

# === Risk & sizing ===
DRY_RUN=false
MAX_DAILY_LOSS_PCT=2.0
USD_EQUITY=250.0
MAX_HISTORY_CANDLES=6000
RISK_PER_TRADE_PCT=15.0          # ↑ larger quote per scalp to matter after taker fees

# === Position controls ===
ALLOW_PYRAMIDING=true
PYRAMID_DECAY_LAMBDA=0.02
PYRAMID_MIN_SECONDS_BETWEEN=0
PYRAMID_MIN_ADVERSE_PCT=1.5
PYRAMID_DECAY_MIN_PCT=0.4
MAX_CONCURRENT_LOTS=8             # ↓ fewer simultaneous scalps

# --- Optional: per-add TP decay for scalp lots ---
SCALP_TP_DECAY_ENABLE=true
SCALP_TP_DEC_MODE=exp
SCALP_TP_DEC_PCT=0.20
SCALP_TP_DECAY_FACTOR=0.9850      # ↑ slightly stronger decay on later adds
SCALP_TP_MIN_PCT=1.9              # floor for later adds

# === Exits (TP/SL + runner trailing) ===
TAKE_PROFIT_PCT=2.5
STOP_LOSS_PCT=1000.00
TRAIL_ACTIVATE_PCT=1.9
TRAIL_DISTANCE_PCT=0.4

# === Fees & exchange minimums ===
FEE_RATE_PCT=0.75
ORDER_MIN_USD=5

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

# === Optional overrides ===
BASE_ASSET=BTC
BASE_STEP=0.00000001

# === Live equity via bridge accounts ===
USE_LIVE_EQUITY=true

# === Order entry mode ===
ORDER_TYPE=limit                 # or "market" (default)
LIMIT_PRICE_OFFSET_BPS=5         # 5 bps (0.05%) improvement from last/side
SPREAD_MIN_BPS=0                 # require ≥1 bps spread to attempt maker
LIMIT_TIMEOUT_SEC=5              # cancel-and-market after 5 seconds if not filled

# === Risk ramp (side-aware) ===
RAMP_ENABLE=true
RAMP_MODE=linear         # or exp
RAMP_START_PCT=3.0       # starts smaller than 15%
RAMP_STEP_PCT=1.0        # linear growth per add on that side
# For exp mode:
# RAMP_GROWTH=1.25
# RAMP_MAX_PCT=15.0

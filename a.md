# === Trading target ===
PRODUCT_ID=BTC-USD
GRANULARITY=ONE_MINUTE

# === Execution cadence (ticks vs candles) ===
USE_TICK_PRICE=true
TICK_INTERVAL_SEC=1
CANDLE_RESYNC_SEC=60

# === Risk & sizing ===
ORDER_MIN_USD=5.00
MAX_DAILY_LOSS_PCT=2.0
RISK_PER_TRADE_PCT=10.0
USD_EQUITY=250.0
MAX_HISTORY_CANDLES=6000

# === Position controls ===
DRY_RUN=false
LONG_ONLY=true
ALLOW_PYRAMIDING=true
PYRAMID_MIN_ADVERSE_PCT=1.5
PYRAMID_DECAY_LAMBDA=0.02
PYRAMID_MIN_SECONDS_BETWEEN=0
MAX_CONCURRENT_LOTS=20
PYRAMID_DECAY_MIN_PCT=0.4

# --- optional: per-add TP decay for scalp lots (OFF by default) ---
SCALP_TP_DECAY_ENABLE=true      # default false; set true to turn on
SCALP_TP_DEC_MODE=exp        # linear | exp   (default linear)
SCALP_TP_DEC_PCT=0.20           # % points to subtract per add (linear), e.g. 0.20
SCALP_TP_DECAY_FACTOR=0.9802      # multiplier per add (exp), e.g. 0.90
SCALP_TP_MIN_PCT=1.6           # floor so TP never gets too tiny, e.g. 0.70
# === Exits (TP/SL + runner trailing) ===
TAKE_PROFIT_PCT=1.9
STOP_LOSS_PCT=1000.00
TRAIL_ACTIVATE_PCT=1.9 
TRAIL_DISTANCE_PCT=0.4

# === Fees & state ===
FEE_RATE_PCT=0.75
STATE_FILE=/opt/coinbase/state/bot_state.json

# === Strategy thresholds ===
BUY_THRESHOLD=0.45
SELL_THRESHOLD=0.55
USE_MA_FILTER=true
BACKTEST_SLEEP_MS=100

# === Ops ===
PORT=8080
BRIDGE_URL=http://bridge:8787
USE_LIVE_EQUITY=true
MODEL_MODE=extended        # baseline | extended
WALK_FORWARD_MIN=1         # 0 disables; 60 = hourly refit in live
VOL_RISK_ADJUST=true       # shrink risk in high vol
DAILY_BREAKER_MARK_TO_MARKET=true
# SLACK_WEBHOOK=https://hooks.slack.com/services/XXX/YYY/ZZZ  # optional
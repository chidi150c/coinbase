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


{
 "EquityUSD": 987.5348955472499,
 "DailyStart": "2025-10-08T00:00:00Z",
 "DailyPnL": -0.1772927661671957,
 "Model": {
  "W": [
   -0.001587933837065381,
   -0.016908617767968873,
   -1.055977726898473,
   0.18619014565879427
  ],
  "B": 0.909343255645928
 },
 "MdlExt": {
  "W": [
   0.007148846178094299,
   -0.00707154310835542,
   0.04156197325831872,
   0.12838940328027756,
   -0.005538890692527039,
   -0.026755240075771472,
   -0.4159422296575638,
   0.0147092154623242
  ],
  "B": 0.20421179240582799,
  "L2": 0.001,
  "FeatDim": 8
 },
 "WalkForwardMin": 1,
 "LastFit": "0001-01-01T00:00:00Z",
 "BookBuy": {
  "runner_id": 0,
  "lots": [
   {
    "OpenPrice": 125325.86,
    "Side": "BUY",
    "SizeBase": 0.00017297,
    "Stop": -1127932.74,
    "Take": 131592.15300000002,
    "OpenTime": "2025-10-05T05:01:32.939508931Z",
    "EntryFee": 0.2601313680504,
    "TrailActive": false,
    "TrailPeak": 125325.86,
    "TrailStop": 0,
    "reason": "pUp=0.86309|gatePrice=0.000|latched=0.000|effPct=0.000|basePct=0.000|elapsedHr=0.0|PriceDownGoingUp=true|LowBottom=false"
   },
   {
    "OpenPrice": 124819.8,
    "Side": "BUY",
    "SizeBase": 0.00023121,
    "Stop": -1123378.2,
    "Take": 127940.295,
    "OpenTime": "2025-10-05T06:21:35.270698108Z",
    "EntryFee": 0.346315031496,
    "TrailActive": false,
    "TrailPeak": 0,
    "TrailStop": 0,
    "reason": "pUp=0.62315|gatePrice=124819.800|latched=0.000|effPct=0.400|basePct=1.500|elapsedHr=1.3|PriceDownGoingUp=false|LowBottom=true"
   },
   {
    "OpenPrice": 124360.71,
    "Side": "BUY",
    "SizeBase": 0.00028972,
    "Stop": -1119286.3499999999,
    "Take": 127412.71800075,
    "OpenTime": "2025-10-05T09:18:47.534912831Z",
    "EntryFee": 0.4323574188144,
    "TrailActive": false,
    "TrailPeak": 0,
    "TrailStop": 0,
    "reason": "pUp=0.57492|gatePrice=124365.150|latched=124580.240|effPct=0.400|basePct=1.500|elapsedHr=2.9|PriceDownGoingUp=true|LowBottom=false"
   },
   {
    "OpenPrice": 124171.05,
    "Side": "BUY",
    "SizeBase": 0.00047764,
    "Stop": -1117538.55,
    "Take": 127153.51150898094,
    "OpenTime": "2025-10-07T06:23:26.710220747Z",
    "EntryFee": 0.711708723864,
    "TrailActive": false,
    "TrailPeak": 0,
    "TrailStop": 0,
    "reason": "pUp=0.67599|gatePrice=124170.950|latched=125092.770|effPct=0.400|basePct=1.500|elapsedHr=11.5|PriceDownGoingUp=false|LowBottom=true"
   },
   {
    "OpenPrice": 123645.21,
    "Side": "BUY",
    "SizeBase": 0.00055901,
    "Stop": -1112625.126,
    "Take": 126535.66715985115,
    "OpenTime": "2025-10-07T08:05:44.914725932Z",
    "EntryFee": 0.8294269061052,
    "TrailActive": false,
    "TrailPeak": 0,
    "TrailStop": 0,
    "reason": "pUp=0.54650|gatePrice=123625.014|latched=0.000|effPct=0.400|basePct=1.500|elapsedHr=1.7|PriceDownGoingUp=true|LowBottom=false"
   },
   {
    "OpenPrice": 123332.61,
    "Side": "BUY",
    "SizeBase": 0.00063965,
    "Stop": -1110180.69,
    "Take": 126200.16414104732,
    "OpenTime": "2025-10-07T14:08:11.08571491Z",
    "EntryFee": 0.946676447838,
    "TrailActive": false,
    "TrailPeak": 0,
    "TrailStop": 0,
    "reason": "pUp=0.57110|gatePrice=123353.410|latched=124005.000|effPct=0.400|basePct=1.500|elapsedHr=6.0|PriceDownGoingUp=false|LowBottom=true"
   },
   {
    "OpenPrice": 121796.64,
    "Side": "BUY",
    "SizeBase": 0.0007207,
    "Stop": -1095910.6500000001,
    "Take": 124522.37131591256,
    "OpenTime": "2025-10-07T15:37:18.728627992Z",
    "EntryFee": 1.053346061376,
    "TrailActive": false,
    "TrailPeak": 0,
    "TrailStop": 0,
    "reason": "pUp=0.71841|gatePrice=121767.850|latched=0.000|effPct=0.400|basePct=1.500|elapsedHr=1.5|PriceDownGoingUp=false|LowBottom=true"
   }
  ]
 },
 "BookSell": {
  "runner_id": 1,
  "lots": [
   {
    "OpenPrice": 122470.03,
    "Side": "SELL",
    "SizeBase": 0.00058692,
    "Stop": 1347170.33,
    "Take": 116346.5285,
    "OpenTime": "2025-10-04T04:26:02.978808939Z",
    "EntryFee": 0.8625613200912,
    "TrailActive": false,
    "TrailPeak": 122470.03,
    "TrailStop": 0,
    "reason": "pUp=0.46223|gatePrice=0.000|latched=0.000|effPct=0.000|basePct=0.000|elapsedHr=0.0|HighPeak=false|PriceUpGoingDown=true"
   },
   {
    "OpenPrice": 122505.98,
    "Side": "SELL",
    "SizeBase": 0.00347451,
    "Stop": 1347565.78,
    "Take": 116380.681,
    "OpenTime": "2025-10-08T08:35:33.195043053Z",
    "EntryFee": 5.1077790308376,
    "TrailActive": false,
    "TrailPeak": 122505.98,
    "TrailStop": 0,
    "reason": "pUp=0.85226|gatePrice=0.000|latched=0.000|effPct=0.000|basePct=0.000|elapsedHr=0.0|HighPeak=false|PriceUpGoingDown=false"
   }
  ]
 },
 "LastAddBuy": "2025-10-08T17:14:14.037360597Z",
 "LastAddSell": "2025-10-08T08:35:47.129066911Z",
 "WinLowBuy": 122853.3,
 "WinHighSell": 124227.5,
 "LatchedGateBuy": 123173.29,
 "LatchedGateSell": 123132.36,
 "LastAddEquitySell": 990.4810752997747,
 "LastAddEquityBuy": 987.5348955472499
}
====================================================================
# === Broker wiring (Binance direct) ===
BROKER=binance
PRODUCT_ID=BTCUSDT
GRANULARITY=ONE_MINUTE
STATE_FILE=/opt/coinbase/state/bot_state.newbinance.json

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
RISK_PER_TRADE_PCT=15.0

# === Position controls ===
ALLOW_PYRAMIDING=true
PYRAMID_DECAY_LAMBDA=0.02
PYRAMID_MIN_SECONDS_BETWEEN=0
PYRAMID_MIN_ADVERSE_PCT=1.5
PYRAMID_DECAY_MIN_PCT=0.4
MAX_CONCURRENT_LOTS=8

# --- Optional: per-add TP decay for scalp lots ---
SCALP_TP_DECAY_ENABLE=true
SCALP_TP_DEC_MODE=exp
SCALP_TP_DEC_PCT=0.20
SCALP_TP_DECAY_FACTOR=0.9852      # ↑ slightly stronger decay on later adds
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
===================================================================

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

# === Exits (profit-gated + trailing/TP behavior) ===
# New profit-gating + USD-based trailing controls:
PROFIT_GATE_USD=0.50              # gate: no exit arm/trigger until net PnL ≥ this
TRAIL_ACTIVATE_USD_RUNNER=1.00    # runner trailing activates at +$1.00 net
TRAIL_ACTIVATE_USD_SCALP=0.50     # scalp trailing activates at +$0.50 net
TRAIL_DISTANCE_PCT_RUNNER=0.40    # runner trailing distance (percent)
TRAIL_DISTANCE_PCT_SCALP=0.25     # scalp trailing distance (percent)
TP_MAKER_OFFSET_BPS=2             # maker-friendly nudge for fixed-TP scalps (optional)

# Legacy % knobs kept for logs/back-compat (won’t arm exits once PROFIT_GATE_USD>0):
TAKE_PROFIT_PCT=2.5
STOP_LOSS_PCT=1000.00
TRAIL_ACTIVATE_PCT=0.0            # neutralize old % activator (USD-based now)
TRAIL_DISTANCE_PCT=0.4            # retained for legacy logging only

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
TICK_SIZE=0.01
# (Optional) ORDER_MIN_USD can remain elsewhere if used by a different check

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
TRACE_EQUITY=true

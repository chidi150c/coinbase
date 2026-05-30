We are auditing whether my Binance BTCUSDT AI model has predictive edge by confidence bucket under its TRUE training objective.

This is a COMPUTATIONAL AUDIT.

Execute calculations exactly.
No hand-waving.
No shortcuts.
Compute first, interpret second.
Even with small samples, still compute all requested metrics.

=========================================================
MODEL CONTEXT
=========================================================

Bot emits:
Raw=BUY / SELL / FLAT

Probability:
pUp

Thresholds:
BUY_THRESHOLD=0.58
SELL_THRESHOLD=0.42

Signal timeframe:
AI_SIGNAL_TF=5m

Label horizon:
AI_LABEL_HORIZON=12

Effective horizon:
60 minutes

Confidence sizing:

CONFIDENCE BUCKETS

BUY:
pUp >= 0.62
0.60 <= pUp < 0.62
0.58 <= pUp < 0.60

SELL:
pUp <= 0.38
0.38 < pUp <= 0.40
0.40 < pUp <= 0.42

Else:
0.00

=========================================================
SOURCE OF TRUTH (TRAINING LABEL)
=========================================================

The model is NOT trained on direction.

LabelType = "path_net_profit"

Parameters:

profitUSD = 1.00
baseUSD = 80.00 fixed for all buckets
feeRatePct = 0.10

Do NOT confidence-scale baseUSD in this audit.
The purpose is to compare prediction quality across buckets using the same target.

Round-trip fee:

fee = 0.002

Targets:

BUY:

buyTarget =
entryPrice ×
(
1 + (1/80) + 0.002
)

≈ +1.45%

SELL:

sellTarget =
entryPrice ×
(
1 - (1/80) - 0.002
)

≈ -1.45%

=========================================================
FORBIDDEN SHORTCUTS
=========================================================

DO NOT test:

future close > entry
future close < entry

DO NOT run a directional audit.

The ONLY valid success criteria:

BUY success:
future HIGH >= buyTarget
within 60 minutes.

SELL success:
future LOW <= sellTarget
within 60 minutes.

=========================================================
INPUT
=========================================================

Bot log containing:

[DEBUG] ... Raw=BUY/SELL/FLAT pUp=...
[TICK] px=...

=========================================================
METHODOLOGY
=========================================================

1. Parse log.

Extract:
timestamp
Raw
pUp
price

2. Remove signal spam.

Count transition-only signals:

FLAT→BUY
FLAT→SELL
BUY→SELL
SELL→BUY

Ignore repeated:
BUY BUY BUY
SELL SELL SELL

3. Ignore FLAT.

4. For each BUY/SELL signal:

Record:
- timestamp
- entry price
- pUp
- confidence bucket

Evaluate ALL future path data for 60 minutes.

BUY:
success if HIGH hits buyTarget.

SELL:
success if LOW hits sellTarget.

If target not hit:
failure.

Also compute:

- time-to-target
- MFE %
- MAE %
- realized move at timeout %

=========================================================
FOR EACH BUCKET COMPUTE
=========================================================

- signal count
- evaluated signal count
- target-hit accuracy %
- target miss %
- average time-to-target
- median time-to-target
- average MFE %
- average MAE %
- average realized move %
- median realized move %

=========================================================
OUTPUT FORMAT
=========================================================

1. Summary table by bucket

2. Strongest buckets ranked

3. Weakest buckets ranked

4. Whether confidence correlates with target-hit success

5. Whether the model objective appears to be learned

6. Exact recommendation:

- keep thresholds?
- tighten thresholds?
- loosen thresholds?
- disable SELL?
- LONG_ONLY?
- abandon model?

=========================================================
STRICT RULES
=========================================================

No vague language.

Bad:
“SELL looked weak.”

Good:
“SELL 0.40–0.42 hit target in 1/9 signals (11.1%).”

If sample size is small:
still compute exact metrics first,
then discuss confidence level separately.

No assumptions.
Actual computed values only.
_______
cd ~/coinbase/monitoring

docker compose logs --since "6h" bot_binance \
| grep -E '\[TICK\] px=|\[DEBUG\] Total Lots=.*Raw=' \
> /tmp/test1_raw_6h.log

cp /tmp/test1_raw_6h.log ~/coinbase/test1_raw_6h.log

wc -l /tmp/test1_raw_6h.log
ls -lh /tmp/test1_raw_6h.log
______
State Reset:

do:

cp /opt/coinbase/state/bot_state.newbinance.json \
/opt/coinbase/state/bot_state.newbinance.json.bak

Then this:

jq '.Model.W=null | .Model.B=0 | .Model.FeatDim=14 | .LastFit="0001-01-01T00:00:00Z"' \
/opt/coinbase/state/bot_state.newbinance.json \
> /tmp/state_reset.json && \
sudo mv -f /tmp/state_reset.json /opt/coinbase/state/bot_state.newbinance.json

And then confirm with this:

jq '.Model,.LastFit' \
/opt/coinbase/state/bot_state.newbinance.json
___________

MODEL PATH:

new candles arrive
↓
BuildFeaturesAndLabels()
↓
new labels mined
(path_net_profit objective)
↓
dataset rebuilt
(old + new labeled rows)
↓
fit()
adjusts weights
↓
weights persisted to state
________________
MINING LABELS:

cd ~/coinbase/monitoring

cd ~/coinbase/monitoring

IMAGE_SHA=0f3628bc6100246daed0bffe620361e3cfbd6361 \
docker compose run --rm --no-deps \
  --entrypoint /app/bot \
  bot_binance \
  -mine-tf 5m \
  -limit 50000


  then do this to check:

wc -l /opt/coinbase/state/mined_labels_binance_5m.jsonl

grep '"y":1' /opt/coinbase/state/mined_labels_binance_5m.jsonl | wc -l
grep '"y":0' /opt/coinbase/state/mined_labels_binance_5m.jsonl | wc -l


docker compose logs --since "10m" bot_binance \
| grep -E 'MINED_LABELS|loaded rows|loaded mined'
why-trade — trading forensic & decision analysis tool
======================================================

why-trade explains why the bot did or did not trade around a
price, timestamp, or order ID.

It searches Binance audit logs, finds the nearest
TRACE step.start, then explains:

  • what the bot saw
  • what the AI predicted
  • what gates blocked/allowed
  • what orders happened
  • whether the decision was correct afterward
    (using future path analysis)

Usage
-----

  why-trade price <PRICE> \
    [-s BUY|SELL] [-l LOGFILE] [-w LINES] \
    [--chunk | --chunk-filter | --after 60m]

  why-trade ts <TIMESTAMP_SUBSTR> \
    [-s BUY|SELL] [-l LOGFILE] [-w LINES] \
    [--chunk | --chunk-filter | --after 60m]

  why-trade oid <ORDER_ID> \
    [-s BUY|SELL] [-l LOGFILE] \
    [--chunk | --chunk-filter]

Description
-----------

why-trade is both:

  1. A forensic tool
     ("What was the bot doing?")

  2. A decision-analysis tool
     ("Was the decision actually correct afterward?")

It searches the Binance audit log, finds the closest trading
step, then analyzes:

  • model prediction (Raw signal)
  • final decision
  • pUp probability
  • gate decisions (MACD / MA / pyramid / equity)
  • order placement
  • funding blocks
  • post-only behavior
  • future path outcome (optional)

Default log:

  /opt/coinbase/logs/audit/binance_audit.log


Modes
-----

price <PRICE>

  Finds the TRACE step.start line whose price is closest
  to PRICE.

Example:

  why-trade price 73586.82 --chunk-filter


ts <TIMESTAMP_SUBSTR>

  Finds the first log line matching the timestamp substring,
  then analyzes the nearby trading step.

Examples:

  why-trade ts 2026-05-31T14:25 --chunk-filter

  why-trade ts 2026-05-31T14:25 \
    -s SELL \
    --after 60m


oid <ORDER_ID>

  Searches current and rotated audit logs for an order ID,
  then walks backward to the nearest step.start.

Example:

  why-trade oid 50919131900 -s BUY --chunk-filter


Flags
-----

-s BUY|SELL

  Side-aware filtering for gate/order patterns.

  Examples:

    -s BUY
    -s SELL

  Default:

    BUY


-l LOGFILE

  Use a different audit log.

  Example:

    -l /opt/coinbase/logs/audit/binance_audit.log


-w LINES

  Half-window context around the matched step.

  Example:

    -w 150

  Default:

    90


--chunk

  Show the full raw step chunk from nearest TRACE step.start
  to the next TRACE step.start.

  Best for:

    deep debugging


--chunk-filter

  Show only key lines from the step chunk:

    • Decision=
    • Raw=
    • pUp=
    • gate blocks
    • order opens/closes
    • post-only activity
    • funding errors
    • pyramid logic
    • HOLD / FLAT

  Best for:

    quick investigation


--after 60m

  Evaluate whether the decision was actually correct
  afterward.

  Uses future price path analysis over the next 60 minutes.

  Why 60m?

    Model training horizon:

      AI_SIGNAL_TF = 5m
      AI_LABEL_HORIZON = 12

    Therefore:

      12 × 5m = 60 minutes

  Output includes:

    • Decision Snapshot
    • Raw signal
    • Final decision
    • pUp
    • gate reasons
    • future price path
    • MFE (max favorable excursion)
    • MAE (max adverse excursion)
    • verdict

Example:

  why-trade ts 2026-05-31T14:25 \
    -s SELL \
    --after 60m


Example output
--------------

Decision Snapshot
-----------------
ts=2026-05-31T14:25:00Z
price=73586.82
Raw=SELL
Decision=FLAT
pUp=0.33924

Reason:
macd_not_strong_positive_for_sell
macd_not_high_peak_for_sell

Path Outcome (+60m)
-------------------
+5m   73582.79  -0.0055%
+10m  73658.49   0.0974%
+15m  73658.49   0.0974%
+20m  73658.49   0.0974%
+30m  73658.49   0.0974%
+45m  73658.49   0.0974%
+60m  73658.49   0.0974%

MFE (SELL): 0.0464%
MAE (SELL): 0.2568%

Verdict:
SELL would likely have failed or was risky.
Gate may have protected the bot.


Typical workflow
----------------

1. Find suspicious decisions

   audit-grep binance \
     'Decision=SELL|Decision=BUY|LIVE ORDER|postonly\.filled' -n

2. Copy the:

   • timestamp
   • price
   • order ID

3. Investigate

   why-trade ts 2026-05-31T14:25 --chunk-filter

4. Evaluate correctness

   why-trade ts 2026-05-31T14:25 \
     -s SELL \
     --after 60m


Relationship to audit-grep
---------------------------

why-trade uses the same audit logs that audit-grep searches.

But:

  audit-grep does NOT produce logs.

The logs are produced by:

  binance-audit-tail.service


Pipeline
--------

bot_binance docker logs
        ↓
binance-audit-tail.service
        ↓
/opt/coinbase/logs/audit/binance_audit.log
        ↓
 ┌──────────────────────────────┐
 │ audit-grep                   │
 │ searches audit logs          │
 └──────────────────────────────┘
        ↓
 ┌──────────────────────────────┐
 │ why-trade                    │
 │ explains & evaluates trades  │
 └──────────────────────────────┘


Mental model
------------

audit-grep answers:

  "What happened?"

why-trade answers:

  "Why did it happen?"

why-trade --after 60m answers:

  "Was it actually the correct decision?"
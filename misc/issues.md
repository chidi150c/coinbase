Trainning a new model:

Option D — Exit Book Classification

Using your exit records.

Features from:

pUp
confidence
MACD
EMA
Pattern
distance from thresholds
profit gate state
exit mode
runner/scalp

Label:

take_profit
vs
trailing_stop
vs
threshold_stop_loss

This predicts:

What kind of exit is this position likely heading toward?

Less useful than A or B in my opinion.

======================================================================================

At the moment, I would say we're actively tracking two major cases.

Case 1 — BUY Decision Quality (mostly resolved, now under observation)

This started because the bot was not producing BUYs for nearly 24 hours.

We investigated:

AI pUp vs BUY threshold
logicEPS
MACDStrongNegative
MACDMomentumUp
EMA pattern
MarketRegime
Confidence scaling of logicEPS

Outcome:

You relaxed the strong-negative criterion to 0.85 × EPS.
logicOpinion=BUY now occurs.
final=BUY occurs.
The bot is posting BUY orders again.

So this case has moved from "debugging" to "monitoring". We now want to verify over multiple trades that the change improves trade quality rather than simply increasing trade frequency.

Case 2 — Maker Execution / Hot Path (active)

This is now the primary focus.

The sequence is:

Price fetched
        ↓
AI/Logic decides BUY
        ↓
Order placed
        ↓
Order repriced (if needed)
        ↓
Filled

We have already identified two sub-problems:

2A. Hot path latency (Case 1G)

You have emphasized this for a while:

Don't introduce delays between price fetch → decision → execution.

We're now instrumenting this with [TRACE] timestamps to measure:

price fetch
decision
order submit
broker response

The goal is to minimize latency in the critical path.

2B. Reprice logic

The logs show:

postonly.reprice.try.new
postonly.reprice.skip.new
reason=no_guard_or_no_improve

repeated while:

Decision=BUY
OPEN-PENDING
status=NEW
reprices=0

So the signal remained valid, but the order never moved. We're now instrumenting maybeRepriceOnce() to understand exactly why it decided not to reprice.

Current roadmap
Monitor the new BUY gate (0.85 × EPS).
Measure hot-path latency from price fetch to order submission.
Instrument and analyze maybeRepriceOnce() to determine why repricing is skipped.
Tune the repricing logic based on the collected diagnostics.

Those are the two active engineering tracks right now, with Case 2 (execution) being the highest priority because the signal generation is now functioning and execution is the limiting factor.
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

Case 3A — SELL Loss Protection

Status: Implemented

Observation

Repeated pattern:

Maker SELL

↓

Loss stop

↓

Taker BUY exit

↓

New SELL below previous loss BUY

↓

Another loss

↓

Repeat

The bot repeatedly sold at worse prices during an UP regime.

Rule Implemented

Only consider

the immediately previous exit

If

previous exit

=

SELL threshold_stop_loss

and

MarketRegime == UP

then

block

SELL entry

below

previous SELL loss BUY exit price
New State

Tracked

LastExitSellLossBuyPrice

LastExitSellLossTime

from the immediately preceding exit record.

Result

The bot no longer repeatedly chases SELL entries downward after an unsuccessful SELL while the market has transitioned into an uptrend.

Case 3B — SELL Loss Recovery

Status: Nearly Complete

This has become the largest engineering feature.

Observation

In a DOWN regime

the previous SELL thesis may still be correct.

The stop-loss may simply have been a temporary adverse move.

Instead of accepting the loss and waiting,

the bot immediately attempts to recover.

Recovery Methods

Two recovery methods were designed.

RecoveryByPositionSize

Recover the loss by increasing the replacement position size.

Formula:

extraBase

×

netProfitPerBase

=

case3BLossUSD

Thus

replacementBase

=

normalBase

+

extraBase

If price returns

stop-loss BUY price

↓

original SELL entry

the additional position recovers the entire realized loss.

RecoveryByProfitTarget

Keep

replacementBase

=

normalBase

but increase

profitGateUSD

=

normalProfitTarget

+

case3BLossUSD

The replacement position remains the same size but stays open longer so that its eventual profit offsets the realized loss.

Recovery Modes

These are execution strategies rather than recovery methods.

Mode A

Enough spare base exists.

Sequence:

Replacement SELL

↓

Loss-exit BUY

↓

Both drain independently.

This minimizes time out of the market.

Mode B

Not enough spare for the larger replacement.

Sequence:

Loss-exit

↓

Attempt replacement immediately
(using RecoveryByProfitTarget)

↓

Return pending

If the replacement fails because of insufficient funds, post-only rejection, timeout, or cancellation:

Remember retry

↓

Retry automatically on later ticks

↓

Clear retry after successful placement

This preserves the opportunity without requiring the replacement to succeed immediately.

New Components Implemented
Replacement request structure.
Recovery method enumeration:
RecoveryByPositionSize
RecoveryByProfitTarget
Retry state:
PendingReplacementRetry
Persistent storage of retry state in BotState.
Automatic retry processor.
Fresh spare-base computation for replacement sizing.
Full-loss recovery sizing (recoveryNetUSD = case3BLossUSD).
Consistent handling for both pending-maker and market/taker exit paths.
Extensive [TRACE] logging for detection, sizing, recovery mode selection, replacement preview, retry creation, retry execution, and replacement lifecycle.
Current Remaining Work

Case 3B is functionally very close to complete. The remaining effort is primarily:

End-to-end validation under live trading conditions.
Verifying retry behavior across exchange responses (fills, cancellations, timeouts).
Confirming that the replacement logic consistently improves realized performance without introducing unintended exposure.

At this point, the project has evolved from primarily tuning entry signals (Case 1) into improving execution quality (Case 2) and intelligent loss management with adaptive recovery (Case 3).

====================================================================
new case 4:

Important: you already fetch balances again after step()

This block performs another balance request:

if trader.cfg.UseLiveEquity() {
	...
	bal, err = fetchBridgeAccounts(...)
	...
	bal, err = fetchBrokerBalances(...)
}

Therefore the bot may now perform:

background balance refresh every second
+
live-equity balance refresh every tick

That is duplicate exchange/bridge I/O.

It does not delay the approved order because your equity refresh happens after step(), but it adds unnecessary API traffic.
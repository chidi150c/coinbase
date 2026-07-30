I would make the completion criteria objective and measurable. A case should only be marked Completed when both the implementation and its intended behavior have been verified.

Case 1 — Entry Decision Quality

Purpose: Improve trade selection by ensuring BUY/SELL entries are opened only when AI, confidence, thresholds, technical indicators, and market regime provide sufficient evidence. Focuses on maximizing long-term expectancy rather than trade frequency.

Completion Criteria
✅ Entry decisions are fully explainable from logs (AI, Logic, confidence, thresholds, MACD, EMA, MarketRegime).
✅ BUY and SELL opportunities occur when expected and are not unintentionally suppressed.
✅ Historical and live analysis confirms improved trade expectancy over the previous implementation.
✅ No significant increase in false entries or unnecessary trades.
✅ No known regressions in entry behavior remain.

Case 2 — Maker Execution & Hot Path

Purpose: Optimize the path from market observation to order placement, minimizing latency and ensuring maker orders are submitted, repriced, and filled as efficiently as possible.

Case 2A — Hot Path Optimization

Reduce all unnecessary delays and blocking operations between price acquisition, decision making, and order submission.

Case 2B — Maker Repricing

Analyze and improve post-only repricing decisions so valid maker orders remain competitive without sacrificing maker execution.

Completion Criteria
✅ No unnecessary blocking operations remain in the decision-to-order path.
✅ Hot-path latency is measured and remains within the target.
✅ Every skipped reprice has an explainable and expected reason.
✅ Maker order fill performance has been validated under live trading.
✅ No unnecessary execution delays remain between decision and order placement.
Case 3 — SELL Loss Management

Purpose: Reduce repeated SELL losses and intelligently recover from unavoidable loss-stop exits.

Case 3A — SELL Loss Protection

Prevent repeated SELL entries below the previous SELL loss-stop BUY exit price while the market is in an UP regime.

Case 3B — SELL Loss Recovery

Automatically recover realized SELL losses through adaptive replacement sizing, profit-target adjustment, and persistent retry mechanisms.

Completion Criteria
✅ SELL loss protection correctly blocks prohibited SELL entries while allowing valid SELL opportunities.
✅ Recovery sizing calculations match the intended formulas.
✅ Recovery modes (A and B) function correctly under live conditions.
✅ Retry logic survives failures and bot restarts.
✅ RecoveryDebtUSD increases and decreases correctly.
✅ Live trading confirms recovery improves overall strategy performance without introducing excessive exposure.
Case 4 — Profit-Giveback Protection

Purpose: Prevent trades that have already reached their profit gate from giving back gains and eventually turning into losing trades by tracking profit peaks and protecting accumulated profit.

Completion Criteria
✅ Protection arms only after the profit gate is reached.
✅ ProfitPeakUSD is tracked correctly.
✅ Protected floor is calculated correctly.
✅ Protection exits occur only under the intended conditions.
✅ All protection exits are correctly classified as L2_PROFIT_PROTECTION.
✅ Protection significantly reduces profitable trades turning into losses.
✅ Any case4.protection_missed events are understood, explainable, and within acceptable limits.
Case 5 — Concurrent Decision Engine (Fan-Out / Fan-In Refactor)

Purpose: Improve scalability and responsiveness by evaluating independent decision components concurrently (AI, MACD, EMA, etc.) using a fan-out/fan-in architecture while preserving legacy behavior, observability, and correctness through verification.

Completion Criteria
✅ Fan-out/fan-in execution is completed for every evaluation cycle.
✅ No goroutine leaks or abandoned evaluations occur.
✅ Decision results are behaviorally equivalent to the legacy implementation.
✅ Legacy logging and observability are fully restored.
✅ Long-duration live testing confirms stable operation without concurrency-related regressions.
✅ The new architecture demonstrably reduces decision latency or improves scalability without changing strategy behavior.
Case 6 — Duplicate Balance Refresh

Purpose: Eliminate unnecessary duplicate broker/bridge balance retrieval caused by both the background balance refresher and the post-step() live-equity refresh, reducing exchange/API traffic while maintaining accurate equity information.

Completion Criteria
✅ Duplicate balance requests are eliminated or explicitly justified.
✅ Balance information remains accurate throughout trading.
✅ Equity calculations remain correct.
✅ No increase in stale-balance decisions is observed.
✅ Exchange/bridge API traffic is reduced without affecting trading correctness.
✅ Live testing confirms no regressions after consolidation.

These completion criteria have a common philosophy: a case is complete only when (1) the implementation is finished, (2) it has been verified under live trading, and (3) it demonstrably achieves its intended objective without introducing regressions. This gives you a clear definition of "done" for every case.


==============================


Case 7 definition

Case 7 — BUY Hold/Recovery Strategy: For BTC spot BUY positions, disable the normal per-trade threshold_stop_loss exit while retaining take-profit, profit protection, trailing-profit exits, account-level risk controls, and all SELL stop-loss behavior. Record every occasion where the former BUY stop threshold is crossed and track subsequent maximum drawdown, recovery time, and eventual exit result.

Do not swap stop-loss and take-profit yet. Do not invert AI yet. Case 7 changes one variable only: BUY threshold-stop execution.

Paste the function or code block that generates threshold_stop_loss; I’ll mark the exact lines to replace without affecting SELL exits.

case 8: swap stop-loss and take-profit
case 9: invert AI

Those are good additions to the experimental roadmap. I'd define them so each changes only one variable, making the results interpretable.

Case 8 — Swap Stop-Loss and Take-Profit Geometry

Objective: Determine whether the current risk/reward geometry is inverted.

Behavior

For every new trade:

Keep the AI decision unchanged.
Keep BUY/SELL direction unchanged.
Keep all entry filters unchanged.
Swap the stop-loss distance and take-profit distance.

Example:

Current:

Take Profit = +1.50 USD
Stop Loss   = -0.80 USD

Case 8:

Take Profit = +0.80 USD
Stop Loss   = -1.50 USD

Everything else remains identical:

AI
confidence
regime
profit protection
trailing
maker logic
sizing

Goal: Determine whether a wider loss allowance and earlier profit-taking improve expectancy.

Case 9 — Invert AI Direction

Objective: Determine whether the AI has learned the opposite polarity.

Behavior

Before any logic fusion:

AI BUY  → AI SELL
AI SELL → AI BUY
AI FLAT → AI FLAT

Important:

Recompute the corresponding thresholds and confidence for the new interpreted direction, rather than simply renaming BUY to SELL. The decision should be internally consistent with the inverted hypothesis.
Keep:
logic filters,
regime,
stop-loss,
take-profit,
profit protection,
sizing,
maker execution
unchanged.

The only variable under test is:

AI direction polarity.

Recommended execution order
Case 7: Disable BUY threshold stop-loss.
Case 8: Swap stop-loss/take-profit distances.
Case 9: Invert AI direction.

Each experiment should be run independently against the same historical period. Avoid combining them initially, because if performance changes, you won't know which modification caused it.

After you've measured each one separately, you can test combinations such as:

Case 7 + Case 8
Case 7 + Case 9
Case 8 + Case 9
Case 7 + Case 8 + Case 9

That progression will tell you not only whether an individual idea works, but also whether combinations produce additive improvements.

==========================================

Case 10 — Stabilize RegimeNormal

Problem observed

Current transition logic allows:

UP
↓ (expired + freshLow)
NORMAL
↓ (next tick freshLow)
DOWN

and similarly:

DOWN
↓ (expired + freshHigh)
NORMAL
↓ (next tick freshHigh)
UP

As a result, RegimeNormal often exists for only one tick and has little practical influence on the bot's behavior.

Goal

Redesign the state machine so RegimeNormal represents a meaningful neutral period rather than an immediate relay between UP and DOWN. The exact confirmation rule (e.g., persistence, second breakout, minimum dwell time, or another criterion) will be determined later.

For now, the roadmap is:

✅ Case 7: Disable BUY threshold_stop_loss (implemented)
⏳ Case 8: Swap stop-loss and take-profit
⏳ Case 9: Invert AI direction
⏳ Case 10: Stabilize RegimeNormal transitions

That keeps the experiments isolated so you can attribute any performance changes to the correct modification.
====================================
Case 11 — Peak-Reversal SELL Producer
Objective

Introduce an independent SELL producer that can detect and act on a potential market top before the normal AI/Logic decision pipeline. The producer is designed to identify high-probability peak reversals by combining momentum exhaustion, price-action confirmation, and the existing SELL pyramid gate.

Motivation

The standard AI/Logic pipeline primarily reacts to established directional signals. Near a market peak, however, momentum often begins to weaken before the AI or threshold-based logic fully transitions to a SELL opinion.

Case 11 provides an earlier, pattern-driven SELL opportunity by recognizing the characteristic signs of a peak reversal while still requiring structural confirmation to reduce false signals.

Entry Criteria

A Case 11 SELL is generated only when all of the following conditions are satisfied:

MACD Pre-Peak Zone
The MACD line from six bars earlier (MACDLinePrev6) is within a configurable buffer of the SELL EPS threshold.
This identifies momentum approaching a bearish transition without requiring it to have fully crossed the threshold.
EMA High-Peak Confirmation
The EMA pattern detector identifies a local high (peak), indicating that upward price movement is stalling or reversing.
SELL Pyramid Gate
The existing SELL pyramid gate passes, confirming that spacing, adverse movement, timing, and other risk-management requirements are satisfied before allowing another SELL.

Only when all three conditions are simultaneously true does Case 11 produce a SELL signal.

Decision Priority

Case 11 has the highest evaluation priority because it is an independent directional producer, not an AI/Logic confirmation gate.

When triggered:

MACD Pre-Peak Zone
        +
EMA High-Peak
        +
SELL Pyramid Gate
        ↓
Case 11
Peak-Reversal SELL Producer
        ↓
DecisionSource = PeakReversal
        ↓
Immediate SELL decision

The decision is returned immediately without continuing through the normal AI/Logic entry evaluation.

Decision Metadata

When Case 11 fires, the decision records:

Signal = Sell
DecisionSource = EntryDecisionSourcePeakReversal
SELL pyramid gate result and reason
SELL equity trigger result and reason

This preserves downstream sizing and auditing behavior while clearly identifying the source of the decision.

Diagnostics

Case 11 emits detailed trace logging containing:

macd_idx6
eps
buffer
threshold
macd_zone
ema_high_peak
pyramid_sell

These diagnostics allow production verification of exactly which conditions contributed to a peak-reversal SELL decision and simplify troubleshooting of missed or unexpected entries.
===================================

Case 12 — Prevent Duplicate Same-Side Entry Submission While Previous Entry Is Pending
Objective

Ensure the bot can have at most one outstanding normal entry per side (BUY or SELL). Once a normal BUY or SELL order has been submitted, subsequent ticks must not submit another normal entry on the same side until the existing pending entry is either:

Filled and committed,
Cancelled,
Expired,
Rejected, or
Otherwise completed and removed from PendingEntries.
Root Cause

Current flow:

Signal = BUY/SELL
        ↓
submitPendingIntent()
        ↓
registerPendingEntry()
        ↓
poller starts
        ↓
(waiting for fill)

During this waiting period:

BookBuy / BookSell still contains zero lots.
The trading logic continues to produce the same valid BUY/SELL decision.
There is no gate that checks for an existing incomplete same-side normal PendingEntry.
Each eligible tick submits another exchange order.

Observed production behavior:

Tick 1
    BUY submitted
    order A = NEW

Tick 2
    BUY still NEW
    BUY submitted again
    order B = NEW

Tick 3
    both fill
    two BUY lots created

The identical behavior occurred for SELL.

Evidence

Observed production logs showed:

pending.register
order_id=A

...

pending.register
order_id=B

while order A was still NEW.

Both subsequently produced:

postonly.filled
lot.created

resulting in duplicate live positions.

Required Behavior

Before any normal BUY submission:

if exists PendingEntry{
        Source == normal &&
        Side == BUY &&
        Completed == false
}
    skip submission

Before any normal SELL submission:

if exists PendingEntry{
        Source == normal &&
        Side == SELL &&
        Completed == false
}
    skip submission
Completion Conditions

The gate is released only when the PendingEntry lifecycle completes, including:

Filled and committed
Cancelled
Timed out
Rejected
Broker failure
Manual cleanup
Success Criteria

For any uninterrupted BUY or SELL signal lasting multiple ticks:

Tick 1
BUY
↓
submit order

Tick 2
BUY
↓
Pending BUY exists
↓
NO submission

Tick 3
BUY
↓
Pending BUY exists
↓
NO submission

...

Fill occurs

↓

Pending removed

↓

Next BUY may be submitted

Invariant

At any time, there shall never be more than one incomplete normal PendingEntry per side, eliminating duplicate exchange orders while preserving legitimate opposite-side entries and specialized flows such as Case 3B.


================================


Case 3B – Mode C (Regime == UP)
Trigger

A recovery intent is created when all of the following are true:

A SELL position exits via threshold_stop_loss.
Case 3B Mode A cannot be funded (insufficient spare base).
Case 3B Mode B is not applicable because MarketRegime != DOWN (i.e., the market is in an UP regime).
Recovery Signal

Wait for a favorable SELL setup:

(emaHighPeakPattern || emaUpGoingDown) &&
StrongPositiveMACD
Behavior
If the recovery signal is already true, immediately submit a post-only maker SELL using RecoveryByProfitTarget.
Otherwise:
Store a pending Mode C recovery intent.
Re-evaluate the recovery signal on each market update.
Submit the replacement immediately once the signal becomes true.
Cancel the pending recovery if the signal does not occur before the configured Signal Timeout (approximately the normal order timeout).
Recovery Method
Entry type: Post-only maker SELL.
Entry price: Current live BBO (not the original stop-loss price).
Position size: Normal trade base.
Profit target: ProfitGateUSD + RecoveryDebtUSD.
Decision Flow
SELL threshold_stop_loss
        │
        ▼
Case 3B Mode A
(Sufficient spare?)
        │
   Yes ─────────► RecoveryByPositionSize
        │
       No
        ▼
Regime == DOWN?
        │
   Yes ─────────► Case 3B Mode B
                  Immediate RecoveryByProfitTarget
        │
       No (UP)
        ▼
Case 3B Mode C
        │
Recovery signal true?
        │
   Yes ─────────► Immediate RecoveryByProfitTarget
        │
       No
        ▼
Store pending recovery
        │
Signal becomes true before timeout?
        │
   Yes ─────────► Submit replacement
        │
       No
        ▼
Cancel pending recovery

This keeps the Case 3B family well organized:

Mode A → Preferred recovery using additional capital (RecoveryByPositionSize).
Mode B → Immediate profit-target recovery in a DOWN regime.
Mode C → Deferred profit-target recovery in an UP regime, waiting for a technically favorable SELL setup.
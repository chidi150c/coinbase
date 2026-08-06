# Trading Bot Design Specification

This document defines the architectural cases that govern the behavior of the trading bot.

Each case has a permanent identifier (for example, Case 3A or Case 13B). Case numbers are never renumbered. Related capabilities may be organized using modes (for example, Case 3A Mode A, Mode B, and Mode C).

The source code remains the implementation source of truth. This document serves as the architectural specification.


I would make the completion criteria objective and measurable. A case should only be marked Completed when both the implementation and its intended behavior have been verified.


---

# Case 1 — Entry Decision Quality

**Section:** Entry Decision Engine

**Status:** Completed



## Purpose

Improve trade selection by ensuring BUY/SELL entries are opened only when AI, confidence, thresholds, technical indicators, and market regime provide sufficient evidence. Focuses on maximizing long-term expectancy rather than trade frequency.


## Completion Criteria
✅ Entry decisions are fully explainable from logs (AI, Logic, confidence, thresholds, MACD, EMA, MarketRegime).
✅ BUY and SELL opportunities occur when expected and are not unintentionally suppressed.
✅ Historical and live analysis confirms improved trade expectancy over the previous implementation.
✅ No significant increase in false entries or unnecessary trades.
✅ No known regressions in entry behavior remain.


---

# Case 2 — Maker Execution & Hot Path

**Section:** Order Execution

**Status:** Completed



## Purpose

Optimize the path from market observation to order placement, minimizing latency and ensuring maker orders are submitted, repriced, and filled as efficiently as possible.


---

# Case 2A — Hot Path Optimization

**Section:** Order Execution

**Status:** Completed


Reduce all unnecessary delays and blocking operations between price acquisition, decision making, and order submission.


---

# Case 2B — Maker Repricing

**Section:** Order Execution

**Status:** Completed


Analyze and improve post-only repricing decisions so valid maker orders remain competitive without sacrificing maker execution.


## Completion Criteria
✅ No unnecessary blocking operations remain in the decision-to-order path.
✅ Hot-path latency is measured and remains within the target.
✅ Every skipped reprice has an explainable and expected reason.
✅ Maker order fill performance has been validated under live trading.
✅ No unnecessary execution delays remain between decision and order placement.

-----

Case 3 — SELL Loss Management

Section: Risk Management & Recovery

Status: In Progress

Purpose

Minimize repeated SELL losses and intelligently recover from unavoidable SELL threshold stop-loss exits through loss protection and adaptive recovery mechanisms.

Case 3B — SELL Loss Protection

Section: Risk Management & Recovery

Status: Completed

Purpose

Prevent repeated SELL entries at worse prices after a SELL threshold stop-loss.

When the market remains in an UP regime and the immediately previous SELL exited via threshold_stop_loss with a realized loss, block any new SELL whose proposed entry price is below the previous SELL exit price.

This enforces:

newSellEntryPrice >= lastSellExitPrice

thereby preventing repeated SELL entries below the previous SELL loss-stop exit price while still allowing valid SELL opportunities.

Case 3A — SELL Loss Recovery

Section: Risk Management & Recovery

Status: In Progress

Purpose

Automatically recover realized SELL losses after SELL threshold stop-loss exits through adaptive replacement mechanisms.

SELL threshold_stop_loss
        │
        ▼
Mode A eligible?
        │
   Yes ─────────► Mode A
        │
       No
        ▼
Regime == DOWN?
        │
   Yes ─────────► Mode B
        │
       No
        ▼
Mode C

Mode A — Recovery by Position Size (Completed)

When sufficient spare base is available, immediately place a replacement SELL using:

Normal position size
Additional recovery size calculated from the realized loss

The replacement position is sized so that returning to the original SELL entry price recovers the realized stop-loss.

Mode B — Recovery by Profit Target (Completed)

When sufficient spare base is unavailable and the market regime is DOWN, place a normal-sized replacement SELL while increasing its profit target by the outstanding recovery amount.

If the initial replacement attempt fails, persistent retry logic continues attempting the replacement until successful.

Mode C — Recovery by Deferred Retry (Under Development)

When neither Mode A nor Mode B is appropriate (for example, outside the DOWN regime), the recovery should remain pending and automatically resume when market conditions become eligible, without abandoning the recovery objective.

Completion Criteria
Completed

✅ Case 3B correctly blocks prohibited SELL entries below the previous SELL loss-stop exit price while allowing valid SELL opportunities.

✅ Case 3A Mode A correctly calculates and applies recovery position sizing.

✅ Case 3A Mode B correctly increases the recovery profit target when Mode A is unavailable.

✅ Replacement retry logic survives temporary failures and bot restarts.

✅ RecoveryDebtUSD increases after realized SELL losses and decreases as recovery profits are realized.

Remaining

⬜ Case 3A Mode C is fully implemented and validated.

⬜ Live trading confirms the complete Case 3 recovery framework (Modes A, B, and C) improves long-term strategy performance without introducing excessive risk.

-----

# Case 4 — Profit-Giveback Protection

**Section:** Risk Management & Recovery

**Status:** Completed



## Purpose

Prevent trades that have already reached their profit gate from giving back gains and eventually turning into losing trades by tracking profit peaks and protecting accumulated profit.


## Completion Criteria
✅ Protection arms only after the profit gate is reached.
✅ ProfitPeakUSD is tracked correctly.
✅ Protected floor is calculated correctly.
✅ Protection exits occur only under the intended conditions.
✅ All protection exits are correctly classified as L2_PROFIT_PROTECTION.
✅ Protection significantly reduces profitable trades turning into losses.
✅ Any case4.protection_missed events are understood, explainable, and within acceptable limits.

---

# Case 5 — Concurrent Decision Engine (Fan-Out / Fan-In Refactor)

**Section:** Decision Engine Architecture

**Status:** Completed



## Purpose

Improve scalability and responsiveness by evaluating independent decision components concurrently (AI, MACD, EMA, etc.) using a fan-out/fan-in architecture while preserving legacy behavior, observability, and correctness through verification.


## Completion Criteria
✅ Fan-out/fan-in execution is completed for every evaluation cycle.
✅ No goroutine leaks or abandoned evaluations occur.
✅ Decision results are behaviorally equivalent to the legacy implementation.
✅ Legacy logging and observability are fully restored.
✅ Long-duration live testing confirms stable operation without concurrency-related regressions.
✅ The new architecture demonstrably reduces decision latency or improves scalability without changing strategy behavior.

---

# Case 6 — Duplicate Balance Refresh

**Section:** Order Execution

**Status:** Completed



## Purpose

Eliminate unnecessary duplicate broker/bridge balance retrieval caused by both the background balance refresher and the post-step() live-equity refresh, reducing exchange/API traffic while maintaining accurate equity information.


## Completion Criteria
✅ Duplicate balance requests are eliminated or explicitly justified.
✅ Balance information remains accurate throughout trading.
✅ Equity calculations remain correct.
✅ No increase in stale-balance decisions is observed.
✅ Exchange/bridge API traffic is reduced without affecting trading correctness.
✅ Live testing confirms no regressions after consolidation.

These completion criteria have a common philosophy: a case is complete only when (1) the implementation is finished, (2) it has been verified under live trading, and (3) it demonstrably achieves its intended objective without introducing regressions. This gives you a clear definition of "done" for every case.


==============================



---

# Case 7 definition

**Section:** Experimental Research

**Status:** Completed



---

# Case 7 — BUY Hold/Recovery Strategy: For BTC spot BUY positions, disable the normal per-trade threshold_stop_loss exit while retaining take-profit, profit protection, trailing-profit exits, account-level risk controls, and all SELL stop-loss behavior. Record every occasion where the former BUY stop threshold is crossed and track subsequent maximum drawdown, recovery time, and eventual exit result.

**Section:** Experimental Research

**Status:** Completed


Do not swap stop-loss and take-profit yet. Do not invert AI yet. Case 7 changes one variable only: BUY threshold-stop execution.

Paste the function or code block that generates threshold_stop_loss; I’ll mark the exact lines to replace without affecting SELL exits.

case 8: swap stop-loss and take-profit
case 9: invert AI

Those are good additions to the experimental roadmap. I'd define them so each changes only one variable, making the results interpretable.


---

# Case 8 — Swap Stop-Loss and Take-Profit Geometry

**Section:** Experimental Research

**Status:** Completed



## Purpose

Determine whether the current risk/reward geometry is inverted.


## Behavior

For every new trade:

Keep the AI decision unchanged.
Keep BUY/SELL direction unchanged.
Keep all entry filters unchanged.
Swap the stop-loss distance and take-profit distance.

Example:

Current:

Take Profit = +1.50 USD
Stop Loss   = -0.80 USD


---

# Case 8:

**Section:** Experimental Research

**Status:** Completed


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


---

# Case 9 — Invert AI Direction

**Section:** Experimental Research

**Status:** Completed



## Purpose

Determine whether the AI has learned the opposite polarity.


## Behavior

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

---

# Case 7: Disable BUY threshold stop-loss.

**Section:** Experimental Research

**Status:** Completed


---

# Case 8: Swap stop-loss/take-profit distances.

**Section:** Experimental Research

**Status:** Completed


---

# Case 9: Invert AI direction.

**Section:** Experimental Research

**Status:** Completed


Each experiment should be run independently against the same historical period. Avoid combining them initially, because if performance changes, you won't know which modification caused it.

After you've measured each one separately, you can test combinations such as:


---

# Case 7 + Case 8

**Section:** Experimental Research

**Status:** Completed


---

# Case 7 + Case 9

**Section:** Experimental Research

**Status:** Completed


---

# Case 8 + Case 9

**Section:** Experimental Research

**Status:** Completed


---

# Case 7 + Case 8 + Case 9

**Section:** Experimental Research

**Status:** Completed


That progression will tell you not only whether an individual idea works, but also whether combinations produce additive improvements.

==========================================


---

# Case 10 — Stabilize RegimeNormal

**Section:** Entry Decision Engine

**Status:** Completed


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

---

# Case 11 — Peak-Reversal SELL Producer

**Section:** Entry Decision Engine

**Status:** Completed


## Purpose



Introduce an independent SELL producer that can detect and act on a potential market top before the normal AI/Logic decision pipeline. The producer is designed to identify high-probability peak reversals by combining momentum exhaustion, price-action confirmation, and the existing SELL pyramid gate.

Motivation

The standard AI/Logic pipeline primarily reacts to established directional signals. Near a market peak, however, momentum often begins to weaken before the AI or threshold-based logic fully transitions to a SELL opinion.


---

# Case 11 provides an earlier, pattern-driven SELL opportunity by recognizing the characteristic signs of a peak reversal while still requiring structural confirmation to reduce false signals.

**Section:** Entry Decision Engine

**Status:** Completed


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


---

# Case 11 has the highest evaluation priority because it is an independent directional producer, not an AI/Logic confirmation gate.

**Section:** Entry Decision Engine

**Status:** Completed


When triggered:

MACD Pre-Peak Zone
        +
EMA High-Peak
        +
SELL Pyramid Gate
        ↓

---

# Case 11

**Section:** Entry Decision Engine

**Status:** Completed

Peak-Reversal SELL Producer
        ↓
Producer = PeakReversal
        ↓
Immediate SELL decision

The decision is returned immediately without continuing through the normal AI/Logic entry evaluation.

Decision Metadata

When Case 11 fires, the decision records:

Signal = Sell
Producer = EntryProducerPeakReversal
SELL pyramid gate result and reason
SELL equity trigger result and reason

This preserves downstream sizing and auditing behavior while clearly identifying the source of the decision.

Diagnostics


---

# Case 11 emits detailed trace logging containing:

**Section:** Entry Decision Engine

**Status:** Completed


macd_idx6
eps
buffer
threshold
macd_zone
ema_high_peak
pyramid_sell

These diagnostics allow production verification of exactly which conditions contributed to a peak-reversal SELL decision and simplify troubleshooting of missed or unexpected entries.
===================================


---

# Case 12 — Prevent Duplicate Same-Side Entry Submission While Previous Entry Is Pending

**Section:** Entry Decision Engine

**Status:** Completed


## Purpose



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

At any time, there shall never be more than one incomplete normal PendingEntry per side, eliminating duplicate exchange orders while preserving legitimate opposite-side entries and specialized flows such as Case 3A.

========================================

---

# Case 13A — Independent Capitulation-Peak SELL Producer

**Section:** Entry Decision Engine

**Status:** Completed


## Purpose



Detect an early SELL opportunity after a prolonged bullish move, when buyers appear exhausted and the first structural evidence of a peak begins to form.

Market Phenomenon


---

# Case 13A models a market where:

**Section:** Entry Decision Engine

**Status:** Completed


AI has already shifted to a SELL bias.
The broader market regime is still UP.
Price remains very close to the recent swing high.
The bullish trend has persisted.
Buying pressure is still present.
The first top structure begins to appear.

This represents an early capitulation peak, where buyers may be exhausting before a reversal.


---

# Case 13B — Independent Capitulation Bottom BUY Producer

**Section:** Entry Decision Engine

**Status:** Completed


## Purpose



Detect and enter an early BUY opportunity after a prolonged bearish move, when the market shows signs of capitulation and the first structural evidence of a bottom begins to form.

Unlike the legacy BUY logic, Case 13 is designed to recognize a specific market phenomenon rather than wait for broader directional agreement.

Market Phenomenon


---

# Case 13 models a market that has been selling for some time and is approaching exhaustion.

**Section:** Entry Decision Engine

**Status:** Completed


Its characteristics are:

AI has already shifted to a BUY bias.
The broader market regime is still DOWN.
Price remains very close to the recent swing low.
The bearish trend has persisted for several candles.
Selling pressure is still present.
The first bottom structure begins to appear.

This combination represents an early capitulation bottom, where sellers may be exhausting before a reversal.

Producer Design
Arm (Environment)

The arm identifies that the market is in a potential capitulation environment.

bottomBuyArm :=
    ai.Raw == Buy &&
    ai.Confidence >= 0.65 &&
    t.MarketRegime == RegimeDown &&
    pyramid.Buy.SpacingPass &&
    priceNearRecentLow &&
    macd.LinePrev6 < 0 &&
    macd.Line < 0 &&
    macd.Hist < 0

This arm does not generate a BUY.

It simply declares:

"The market is now in a capitulation environment."

Trigger

The final confirmation is the emergence of a bottom structure.

bottomBuy :=
    bottomBuyArm &&
    ema.LowBottom

Only when the environment already exists and EMA confirms a bottom does the producer emit a BUY signal.

Raw Materials
Purpose	Raw Material
Direction	ai.Raw == Buy
Signal Quality	ai.Confidence >= 0.65
Market Context	t.MarketRegime == RegimeDown
Entry Protection	pyramid.Buy.SpacingPass
Price Location	priceNearRecentLow
Trend Persistence	macd.LinePrev6 < 0
Current Trend	macd.Line < 0
Selling Pressure	macd.Hist < 0
Structural Confirmation	ema.LowBottom
Price Near Recent Low


---

# Case 13 introduces a reusable price-location feature.

**Section:** Entry Decision Engine

**Status:** Completed


nearLowPct :=
    (price - t.RecentLow) /
        t.RecentLow * 100.0

priceNearRecentLow :=
    t.RecentLow > 0 &&
    nearLowPct >= 0 &&
    nearLowPct <= 0.10

This ensures the producer only activates while price remains within 0.10% above the recent low, preventing late entries after price has already moved away from the bottom.

Decision Source
EntryProducerCase13BBottomBuy
Characteristics
Independent BUY producer.
Focused on one market phenomenon.
Uses the minimum raw materials required.
Separates the persistent market environment (Arm) from the structural confirmation (Trigger).
Does not modify or depend on the legacy BUY producer.
Expected Behavior


---

# Case 13 should fire in situations where:

**Section:** Entry Decision Engine

**Status:** Completed


the market has experienced a sustained decline,
AI anticipates a reversal,
price is still sitting near the recent low,
bearish momentum has persisted,
and the first bottom structure appears,

allowing the bot to participate earlier in a developing reversal while retaining structural confirmation before entering.

================================



---

# Case 3A – Mode C (Regime == UP)

**Section:** Risk Management & Recovery

**Status:** Completed

Trigger

A recovery intent is created when all of the following are true:

A SELL position exits via threshold_stop_loss.

---

# Case 3A Mode A cannot be funded (insufficient spare base).

**Section:** Risk Management & Recovery

**Status:** Completed


---

# Case 3A Mode B is not applicable because MarketRegime != DOWN (i.e., the market is in an UP regime).

**Section:** Risk Management & Recovery

**Status:** Completed

Recovery Signal

Wait for a favorable SELL setup:

(emaHighPeakPattern || emaUpGoingDown) &&
StrongPositiveMACD

## Behavior
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

## Decision Flow
SELL threshold_stop_loss
        │
        ▼

---

# Case 3A Mode A

**Section:** Risk Management & Recovery

**Status:** Completed

(Sufficient spare?)
        │
   Yes ─────────► RecoveryByPositionSize
        │
       No
        ▼
Regime == DOWN?
        │
   Yes ─────────► Case 3A Mode B
                  Immediate RecoveryByProfitTarget
        │
       No (UP)
        ▼

---

# Case 3A Mode C

**Section:** Risk Management & Recovery

**Status:** Completed

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

This keeps the Case 3A family well organized:

Mode A → Preferred recovery using additional capital (RecoveryByPositionSize).
Mode B → Immediate profit-target recovery in a DOWN regime.
Mode C → Deferred profit-target recovery in an UP regime, waiting for a technically favorable SELL setup.

---

# Case 14B — Uptrend BUY Producer

**Section:** Entry Decision Engine

**Status:** Completed

## Purpose

Provide an independent BUY producer that participates in an established UP market by identifying qualified trend-continuation BUY opportunities without altering the existing producer architecture.

## Behavior

- Evaluates the Case 14B trend-continuation conditions.
- Generates a BUY decision only when all required conditions are satisfied.
- Routes the decision through the standard producer lifecycle (Decision → Pending → Filled → Committed or Failed).
- Preserves existing sizing, auditing, and execution behavior.

## Decision Flow

Market Evaluation
        │
        ▼
Case 14B Conditions Satisfied?
        │
   Yes ─┴─ No
    │        │
    ▼        ▼
Create BUY   No Action
Decision
    │
    ▼
Pending
    │
    ▼
Filled
    │
    ▼
Committed / Failed

## Completion Criteria

✅ Case 14B produces BUY decisions only when its qualifying conditions are satisfied.
✅ Decisions pass through the standard producer lifecycle.
✅ All decisions are fully observable through BOT OPS Producer Monitor.
✅ Live validation confirms the producer behaves as intended without regressions.

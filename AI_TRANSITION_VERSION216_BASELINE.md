# AITransitionTrader — version216 authoritative behavior baseline

This baseline describes the current version216 source, including the agreed
AITransition hold/giveback update. It is the reference for v2 porting. The new
funding reporting patch and packaged source still require Go tests/build before
this candidate baseline is deployed or promoted as verified.

## Model input and parallel operation

The legacy AI continues serving existing producers. With
AI_TRANSITION_30_ENABLED=true, retained AITransitionTrader lots use the
30-feature model's raw BUY/SELL/FLAT class for transitions. The new model is
24 engineered one-minute features plus six one-hot cluster membership features.
The trained model and cluster artifact carry their own preprocessing parameters.
The configured artifact is AI30ClusterOneMinute180dLBFGS.

step.go evaluates the new AI in a goroutine alongside the legacy AI, MACD and
EMA work. It consumes the existing candle snapshot and tick price. Candle-gap
repair remains outside the step path. No additional price/balance/candle fetch
is introduced for deciding the rollover.

When enabled but unavailable or inference fails, AI transitions are skipped;
there is no silent fallback to the legacy model. A previously latched giveback
can still supply its own rollover signal. With the new-model toggle disabled,
the existing legacy transition path applies.

The artifact's BUY and SELL confidence gates still affect its raw class.
The new model returns confidence, but retained-lot rollover does not add a
producer-level confidence threshold. FLAT confidence is returned as zero.

## One-time seed

The seed continues to use legacy AI raw and confidence through the standard
producer pipeline. Eligible transitions are FLAT/SELL -> BUY or FLAT/BUY -> SELL.
A seed is allowed only while AITransitionInitialized=false and neither side has
a pending seed. Confirmed entry initializes the producer; restart reattaches its
existing state instead of seeding again. AI_TRANSITION_SEED_USD controls sizing
(source default 70 USD); this is separate from the training label notional.
Do not infer that the producer trades 100 USDT just because training labels did.

## Retained lot and accepted transitions

A BUY lot rolls to SELL on a fresh FLAT/BUY -> SELL raw transition.
A SELL lot rolls to BUY on a fresh FLAT/SELL -> BUY raw transition.
The lot is retained until its selected rollover executes. An accepted transition
is captured as pending before previous raw advances, including during the hold.

The separate previousAITransitionRaw persists for the new model. The existing
legacy previousAIRaw remains independent. afterStepStateUpdate advances the new
value when StepResult.TransitionValid=true in the current implementation.

An unsubmitted AI pending rollover is cancelled when successful AI evaluation
stops supporting its direction. An inference error does not count as a changed
opinion. An exchange-owned exit is handled by its existing execution lifecycle.
The giveback signal is separately latched and is not cancelled by a FLAT raw AI.

## Holding clock, net target and giveback

| Condition | Behavior |
| --- | --- |
| Less than 100 minutes and target not armed | Capture accepted transitions, but do not submit rollover. |
| Specific lot reaches at least 1 USDT estimated net profit | Arm the target and unlock early. |
| Unlocked and an accepted AI transition is eligible | Permit rollover subject to the remaining guards. |
| Target armed, no accepted transition | Keep the lot open and track the highest net profit. |
| Net profit falls at least 0.20 USDT from that peak | Latch a giveback rollover toward the opposite side, including when raw AI is FLAT. |
| 100 minutes reached, target not met | Unlock the hold; expiry alone does not close the lot. Wait for an accepted transition. |

The clock starts at the lot's confirmed OpenTime, not order submission.
Confirmed rollover creates/updates the opposite lot and restarts its clock.
Confirmed Case3C augmentation, including later contributing partial fills,
restarts the consolidated lot clock and resets target/peak/giveback state.

The 1 USDT target is specific-lot profit after entry fee and estimated exit fee,
not gross profit, account-wide PnL, or order notional. It is independent of
PROFIT_GATE_USD. Peak tracking remains active once armed, including after expiry.
A giveback trigger requests execution; maker/fill latency means final realized
profit is not guaranteed to equal the mark-time profit.

## Losing rollover and regime direction

This table applies after unlocking, to AI-driven rollovers with negative net.

| Regime | Existing lot | Destination trade | Allowed? |
| --- | --- | --- | --- |
| UP | SELL | BUY | Yes: destination follows UP. |
| UP | BUY | SELL | No: keep pending while loss/regime guard applies. |
| DOWN | BUY | SELL | Yes: destination follows DOWN. |
| DOWN | SELL | BUY | No: keep pending while loss/regime guard applies. |
| NORMAL | Either | Opposite side | Existing profit-price guard must pass. |

NORMAL uses activationPrice to cover source cost, entry fee, estimated exit fee,
and PROFIT_GATE_USD * LowTierProducerMultiplier (0.5). With gate 0.7, that guard
is 0.35 USDT; it is not the separate 1 USDT early-unlock target.

A latched giveback acts as its own rollover signal and bypasses the AI losing-
rollover/NORMAL profit-price guards in current code. It does not bypass exchange
ownership, funding, retry timing, or execution reconciliation.

## Case3C and execution

Case3C same-side recovery augmentation continues during the hold. It does not
need the hold to expire. Its loss trigger uses existing configuration and its
obligation, reservation, consolidation and recovery accounting are preserved.
A blocked AI transition does not suppress Case3C's independent eligibility.

Retained-lot rollovers use the existing maker/post-only exit path with its
configured timeout and fallback behavior. They are not all immediate market
orders. The candidate applies TPMakerOffsetBps in the closing direction.
AI rollover submission retries retain the existing 30-second timing.
The current selection authorizes at most one retained AITransition lot per tick.

The source lot's recovery chain transfers to its confirmed destination. Actual
realized PnL contributes once to the chain; no duplicate seed, debt or lifecycle
record should be created by a retried decision or partial fill.

## State that must survive restart

- previous legacy and new-model raw classes, separately.
- AITransitionInitialized.
- Lot OpenTime, identity, side, size, cost and entry fees.
- AITransitionRolloverPending and AITransitionNextRetryAt.
- AITransitionTargetArmed, AITransitionPeakNetUSD, AITransitionGivebackPending.
- Pending exits/entries and exchange ownership.
- Case3C obligations/retries and resource reservations.
- Producer history, performance economics and order lineage.

Required lifecycle durability boundaries remain in place. Routine funding
reporting is covered by VERSION216_FUNDING_BASELINE.md; it adds no model fetch.

## Observability actually implemented

Executed rollover decision reasons include previous_ai, current_ai, resume,
regime, hold_expired, profit_giveback, peak_net_usd and NORMAL guard parameters.
The decision is carried to the exit record and destination-entry producer events.

AI_SHADOW records model class, confidence, probabilities, cluster and decision ID.
That shadow decision ID is not attached as model lineage to producer events in
this baseline. Producer events use their existing producer/order lineage.

Waiting during a hold, target arming, an expired lot without a transition, or a
blocked regime does not continuously create BOT OPS producer events. Some
waiting/cancellation debug logs exist. Do not describe this as complete live
hold/giveback visibility in BOT OPS. Site rendering was not changed here.

## v2 implementation requirements

Port the above behavior as an explicit producer policy. Keep inference model
selection independent from execution policy and allow additional models/producers
without a fixed two-model limit. Active model evaluation should run in parallel
against an immutable tick snapshot. Each installed producer retains its own raw
history, positions, pending signals, recovery state, decisions and economics.
Shared market data may be selected per configuration; do not hardwire sharing.

Preserve explicit seed selection and confidence behavior. Do not silently change
seed to the new model while porting. Do not add EMA/MACD/pyramid/equity admission
gates to retained-lot rollover. Preserve exchange rules, reservations, idempotent
fill accounting and recovery state ownership. Use one timestamped funding
snapshot for allocation and reporting, rather than incremented spare counters.

This document establishes the porting contract. It does not claim the v2 live
AITransition producer has already been implemented or installed.

## Source map and acceptance checks

| Source | Responsibility |
| --- | --- |
| ai_shadow_30.go / ai_shadow_indicators.go | New model, preprocessing, clustering, raw output, worker history. |
| strategy.go | One-time seed collection and afterStepStateUpdate. |
| step.go | Parallel inference, accepted transition selection, hold and regime guards. |
| ai_transition_hold.go | 100-minute clock, 1 USDT target, 0.20 giveback, destination loss rule. |
| trader.go | Durable fields, confirmed fill clocks, rollover and Case3C consolidation. |
| observability.go | Producer lifecycle and economics. |
| resource_manager.go / producer_resource_coordinator.go | Reservation ownership and resource allocation. |
| live.go | Startup model selection and tick operation. |

Existing hold tests cover 99/100-minute boundaries, early unlock, peak giveback,
and losing direction by regime. Existing recovery tests cover Case3C accounting.
Before promotion run full tests, race checks and build. Add integration coverage
for pending cancellation during hold, FLAT giveback, restart with armed target,
failed/partial maker exits and augmentation clock resets as v2 is ported.

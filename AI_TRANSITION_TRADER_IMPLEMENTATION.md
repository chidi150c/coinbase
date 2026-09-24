# AITransitionTrader Implementation Specification

## 1. Purpose

This document is a portable implementation contract for reproducing the complete `AITransitionTrader` behavior in another repository, especially `coinbase-v2`.

It describes the required behavior, ownership rules, state, exchange accounting, Producer Monitor lifecycle, NORMAL-regime protection, rollover execution, Case3C loss-averaging recovery, persistence, restart behavior, reset behavior, and tests.

The implementation should reuse the target repository's existing order, pending-entry, pending-exit, resource-allocation, persistence, and Case3 obligation machinery. Do not create a second trading pipeline merely for `AITransitionTrader`.

## 2. Behavioral contract

`AITransitionTrader` maintains one directional position chain driven by changes in the raw AI opinion.

Both activation forms are intentional:

- `FLAT -> BUY`
- `FLAT -> SELL`
- `BUY -> SELL`
- `SELL -> BUY`

Do not change it to only `BUY <-> SELL`. The current design uses both directional activation and direct directional reversal.

At startup, when no AITransition position exists, a qualifying raw AI direction creates a seed entry. After a confirmed seed fill, future opposite transitions use the exit fill of the current lot as the entry fill of the opposite lot. This is called a rollover.

The strategy must never create an opposite local lot from an unconfirmed order. Exchange-confirmed filled quantity is authoritative.

## 3. Core invariants

The implementation must preserve these invariants:

1. There is at most one authoritative AITransition directional chain.
2. A seed lot is created only from a confirmed seed entry fill.
3. A rollover destination is created only from a confirmed source exit fill.
4. The rollover exit order ID becomes the destination lot's entry order ID.
5. Partial rollover fills move only confirmed quantity.
6. The unfilled source residual remains owned by the source lot.
7. Reprocessing the same confirmed fill must not create a duplicate destination lot or duplicate Producer Monitor attempt.
8. `FLAT -> BUY/SELL` and `BUY <-> SELL` transitions remain supported.
9. NORMAL-regime protection may defer a transition; UP and DOWN retain immediate transition behavior.
10. Case3C applies only to AITransition lots in `NORMAL`.
11. A Case3C augmentation is same-side and does not close its source lot.
12. A Case3C obligation survives rollovers until its recovery balance is paid.
13. Exchange quantities, commissions, resource reservations, and local lot ownership must be accounted for exactly once.

## 4. Required state

Use the target repository's equivalent types and naming where they already exist.

### 4.1 Trader-level state

Required durable fields:

```go
PreviousAIRaw           Signal
AITransitionInitialized bool

Case3CObligations    map[string]*Case3AObligation
PendingCase3CRetries map[string]PendingReplacementRetry
```

`PreviousAIRaw` is the previously committed raw AI opinion used to detect a transition.

`AITransitionInitialized` indicates that the strategy has established its chain. It must be restored from persisted state.

Case3C should reuse the existing Case3 obligation record and retry record. Keep Case3C in separate maps so Case3A, Case3B, and Case3C ownership remains explicit.

### 4.2 Position fields

The position type needs these fields or their existing equivalents:

```go
Producer                  EntryProducer
ProducerReason            string
EntryOrderID              string
AITransitionCapitalUSD    float64
AITransitionRolloverPending bool
AITransitionNextRetryAt   time.Time

RecoveryObligationID      string
RecoveryNetUSD            float64
RecoveryMethod            RecoveryMethod

Case3AReplacementStarted  bool
Case3AReplacementOrderID  string
```

The existing Case3 replacement flags may be reused for Case3C augmentation submission ownership. Renaming them is optional and should not be required merely for Case3C.

### 4.3 Pending intent fields

Reuse the existing pending intent:

```go
Producer                     EntryProducer
DecisionID                   string
SourceEntryOrderID           string
ObligationID                 string
RecoveryAugmentation         bool
ConsolidationEntryOrderID    string
AugmentationTriggerLossUSD   float64
RecoveryNetUSD               float64
RecoveryMethod               RecoveryMethod
ProfitGateUSD                float64
```

For Case3C:

- `Producer = AITransitionTrader`
- `RecoveryAugmentation = true`
- `ConsolidationEntryOrderID = source AITransition entry order ID`
- `SourceEntryOrderID = source AITransition entry order ID`

### 4.4 Case3 obligation extension

Use the existing obligation structure. Add this field:

```go
UnrealizedTriggerUSD float64 `json:"unrealized_trigger_usd,omitempty"`
```

This field prevents double counting. Case3C initially records an unrealized trigger loss. If a later rollover realizes that same loss, consume `UnrealizedTriggerUSD` before adding any excess realized loss to the recovery obligation.

## 5. Producer identity and priority

Use one producer identity:

```go
EntryProducerAITransitionTrader
```

Do not invent separate producer names for seed, rollover, and Case3C augmentation. Those are lifecycle modes of the same producer.

The existing producer-priority function remains authoritative. Case3C augmentation should enter the existing resource coordinator through the AITransition producer identity and its existing priority unless the target repository already has a documented recovery priority override.

## 6. Seed entry

### 6.1 Qualification

When no AITransition lot exists and the raw AI is directional:

```text
Raw AI = BUY  -> request BUY seed
Raw AI = SELL -> request SELL seed
Raw AI = FLAT -> do nothing
```

Use the configured seed capital, exchange filters, existing resource coordinator, and ordinary entry path.

### 6.2 Reason

Every reason must include the current regime:

```text
ai_transition_seed|previous_ai=<...>|current_ai=<...>|regime=<NORMAL|UP|DOWN>|seed_usd=<...>
```

The ordinary Producer Monitor stage enrichment may append:

```text
stage=<stage>|stage.elapsed_ms=<n>|hotpath.elapsed_ms=<n>
```

### 6.3 Commit

After confirmed fill:

- create the lot through the existing entry commit path;
- set `Producer = AITransitionTrader`;
- set `EntryMethod = AITransitionTrader`;
- set `AITransitionCapitalUSD` from committed quote ownership;
- set `AITransitionInitialized = true`;
- persist state.

## 7. Transition detection

Use raw AI, not the final combined producer decision.

```go
transitionToBuy := raw == Buy &&
    (previousAIRaw == Sell || previousAIRaw == Flat)

transitionToSell := raw == Sell &&
    (previousAIRaw == Buy || previousAIRaw == Flat)
```

For a current BUY lot, only `transitionToSell` qualifies.

For a current SELL lot, only `transitionToBuy` qualifies.

Do not remove `FLAT -> direction` qualification.

## 8. Regime behavior

### 8.1 UP and DOWN

In non-NORMAL regimes, an opposite raw-AI transition may immediately authorize rollover through the existing exit path.

No NORMAL profit requirement is applied.

### 8.2 NORMAL profit protection

In `NORMAL`, a rollover must not submit until the source lot covers:

- source entry cost;
- entry commission;
- estimated exit commission;
- configured low-tier net-profit requirement.

Use the existing fee-aware activation-price/profit-gate helper. Do not create a second fee formula.

The reason should expose the decision:

```text
producer=AITransitionTrader
previous_ai=<...>
current_ai=<...>
resume=<true|false>
regime=NORMAL
normal_profit_protected=<true|false>
normal_required_net_usd=<...>
normal_required_price=<...>
```

If the opposite transition occurs before the profit requirement is met:

- set `AITransitionRolloverPending = true`;
- retain the source lot;
- retry at later tick boundaries;
- do not manufacture an exit or opposite lot.

If the raw AI stops supporting the pending direction before an exchange order owns it, the unsubmitted NORMAL rollover may be cancelled:

```go
lot.AITransitionRolloverPending = false
lot.AITransitionNextRetryAt = time.Time{}
```

Once an exchange order owns the rollover, reconciliation—not a raw-signal change—owns the outcome.

## 9. Rollover execution

### 9.1 Source exit

Use the existing maker exit path and pending-exit machinery.

Reason prefix:

```text
ai_transition_rollover|regime=<...>
```

The source position is not removed until the exchange reports a confirmed fill.

### 9.2 Confirmed fill becomes destination entry

For a confirmed source exit:

```text
source BUY exit by SELL -> destination SELL lot
source SELL exit by BUY -> destination BUY lot
```

The same exchange fill has two accounting roles:

1. realized exit of the source lot;
2. entry basis of the opposite AITransition lot.

Use:

```go
destination.EntryOrderID = exitOrderID
```

Destination reason:

```text
ai_transition_rollover_fill|regime=<...>|source_entry_order_id=<...>|exit_order_id=<...>
```

### 9.3 Partial fill behavior

For a partial fill:

- create or enlarge the destination using only the confirmed filled quantity;
- reduce the source lot by the confirmed gross exit quantity;
- proportionally reduce source entry fee and AITransition capital;
- retain `AITransitionRolloverPending = true` on the source residual;
- set a retry time;
- keep source and destination order identities stable;
- persist before another retry can submit.

Repeated partial fills for the same destination must consolidate, not create several destination lots.

### 9.4 Full fill behavior

For a full fill:

- record realized source PnL;
- remove the source lot;
- retain the confirmed destination lot;
- clear source pending-exit ownership;
- mark the source Producer Monitor attempt exited;
- persist state.

## 10. Exchange commission accounting

### 10.1 Bridge response

Every fill emitted by the Binance bridge must include:

```json
{
  "price": "...",
  "size": "...",
  "fee": "...",
  "commission": "...",
  "commissionAsset": "BTC|USDT|BNB|..."
}
```

Do not infer the fee asset from the order side.

### 10.2 Broker result

Extend the order result:

```go
CommissionUSD  float64
CommissionBase float64
```

`BaseSize` remains gross executed quantity so VWAP and exchange reconciliation remain correct.

`CommissionBase` is the total commission charged in the product's base asset.

### 10.3 Net credited base

Use one helper:

```go
func netCreditedBase(side OrderSide, grossBase, commissionBase float64) float64 {
    if side != SideBuy || commissionBase <= 0 {
        return grossBase
    }
    return math.Max(0, grossBase-commissionBase)
}
```

Apply it exactly once when a BUY lot takes ownership of a confirmed fill.

Do not modify SELL base ownership using this helper.

### 10.4 Poll aggregation

Order-status responses may be cumulative. Accumulate only deltas:

```go
dCommissionBase := order.CommissionBase - lastSeenCommissionBase
if dCommissionBase < 0 {
    dCommissionBase = 0
}
sessionCommissionBase += dCommissionBase
lastSeenCommissionBase = order.CommissionBase
```

The final aggregate order must carry `sessionCommissionBase` into commit.

This is required for both pending entries and pending exits.

## 11. Producer Monitor model

### 11.1 Demarcation

Each confirmed rollover represents a new AITransition entry attempt.

The demarcation is:

```text
one confirmed rollover destination entry order ID = one ProducerAttempt
```

Do not keep one ProducerAttempt alive forever from the initial seed.

The source attempt receives its realized PnL and terminal exit stage. The destination receives a new linked attempt.

### 11.2 Rollover stages

Because the exchange fill is already confirmed when the destination is created, record these canonical stages retrospectively with the same order ID and fill time:

```text
decision
exchange_accepted
filled
committed
```

Every stage must contain:

- `Producer = AITransitionTrader`
- destination side;
- destination entry order ID;
- execution price;
- owned base quantity;
- quote value;
- rollover reason containing source and destination IDs.

Recommended reason:

```text
ai_transition_rollover_entry|source_entry_order_id=<source>|entry_order_id=<destination>|<exit-decision-details>
```

### 11.3 Idempotency

Before recording a rollover attempt, search existing AITransition attempts by entry order ID.

If already present, do nothing.

Decision IDs may retain the standard producer-plus-millisecond format. If the millisecond ID collides, append the exchange order ID rather than inventing an unrelated identity scheme.

## 12. Case3C: AITransition loss-averaging recovery

### 12.1 Meaning

Case3C is the AITransition-specific use of the existing Case3 Loss-Averaging Recovery method.

It is not an independent trading producer or a replacement for AI transitions. It is a durable recovery obligation attached to the AITransition chain.

### 12.2 Trigger

Case3C triggers only when all are true:

```text
lot producer == AITransitionTrader
market regime == NORMAL
threshold stop-loss enabled
fee-aware net PnL <= configured negative loss threshold
no duplicate augmentation already owns the lot
```

Evaluate this before adding the lot to normal AI rollover candidates for that tick.

The threshold candidate should enter the existing `closeLot`/Case3 decision path using:

```text
reason=threshold_stop_loss
exit_class=CASE3C_AUGMENTATION
```

The word `exit` here identifies the existing decision path. A successful Case3C decision suppresses the loss exit and submits same-side augmentation.

### 12.3 Reuse of Case3 machinery

Generalize existing Case3 helpers with predicates such as:

```go
func isRecoveryReplacementProducer(p EntryProducer) bool {
    return p == Case3AReplacement || p == Case3BReplacement
}

func isRecoveryObligationProducer(p EntryProducer) bool {
    return isRecoveryReplacementProducer(p) ||
        p == AITransitionTrader
}

func isRecoveryObligationIntent(p EntryProducer, intent *PendingIntent) bool {
    return isRecoveryReplacementProducer(p) ||
        (p == AITransitionTrader && intent != nil && intent.RecoveryAugmentation)
}
```

Use the broader obligation predicate only in obligation-aware paths. Do not globally classify every ordinary AITransition seed or rollover as a Case3 entry.

The generalized paths include:

- obligation creation;
- pending-entry registration;
- commit eligibility;
- fill apportionment;
- consolidation;
- retry/reconciliation handling;
- restart reattachment;
- state-derived resource reservations.

### 12.4 Augmentation construction

For an AITransition lot in NORMAL:

```go
replacementProducer = EntryProducerAITransitionTrader
reasonPrefix = "case3C"
recoveryAugmentation = true
```

Construct the pending intent using the source lot identity and the existing Case3 Mode A/Mode B sizing logic.

Mode A and Mode B definitions remain the existing Case3 definitions:

- Mode A: increase position size enough to recover the obligation over the intended recovery move when sufficient spare resources exist.
- Mode B: use ordinary same-side size and carry the loss into the required profit target when Mode A cannot be funded.

Do not rewrite those formulas for Case3C.

### 12.5 Source ownership

Once Case3C is selected:

- create or reuse one durable obligation;
- reserve its resources through the existing ledger;
- submit the same-side augmentation through the existing entry pipeline;
- set the source lot's replacement-started/order-ID fields;
- suppress the source loss exit.

If augmentation submission fails with confirmed zero fill, retain the source lot and return the obligation to the appropriate retry/wait state.

If submission outcome is uncertain, move the obligation to reconciliation. Do not submit a duplicate.

### 12.6 Confirmed augmentation fill

Do not append another independent lot.

Consolidate the confirmed fill into the source AITransition lot:

```text
new base          = old base + net credited augmentation base
new cost/notional = old cost + confirmed augmentation cost
new open price    = new cost / new base
new entry fee     = old entry fee + augmentation entry fee
new recovery      = old recovery + proportional trigger loss
new profit gate   = old profit gate + proportional augmentation gate
```

Clear temporary replacement-started ownership after authoritative consolidation.

Partial augmentation fills must apply only their proportional trigger loss and profit gate. The unfilled remainder stays under the same obligation.

### 12.7 When the obligation begins

The initial seed or initial AI transition does not automatically create a Case3C obligation.

The obligation begins only when:

1. an AITransition lot crosses the configured loss threshold in NORMAL;
2. the Case3C decision is created;
3. the durable obligation is persisted;
4. confirmed augmentation fills make its recovery economics authoritative.

It then follows the AITransition chain until completed.

### 12.8 Rollover transfer

When a lot carrying Case3C rolls to the opposite side:

- do not create another obligation;
- transfer the same obligation ID to the confirmed destination lot;
- update the obligation's source/consolidated entry order ID to the rollover exit order ID;
- update obligation side to destination side;
- keep status `position_open` while unpaid.

### 12.9 Realized PnL accounting

Apply authoritative rollover net PnL using:

```go
func applyCase3CRolloverPnL(o *Case3AObligation, realizedNet float64) {
    if realizedNet < 0 {
        loss := -realizedNet
        preview := math.Min(loss, math.Max(0, o.UnrealizedTriggerUSD))
        o.UnrealizedTriggerUSD -= preview
        o.RecoveryRemainingUSD += loss - preview
        o.RecoveryOriginalUSD += loss - preview
    } else if realizedNet > 0 {
        o.RecoveryRemainingUSD -= realizedNet
    }
}
```

Meaning:

- the portion already represented by the unrealized trigger is converted to realized without being added twice;
- any excess realized loss increases the obligation;
- realized profit reduces the obligation.

Global `RecoveryDebtUSD` may continue to receive the exit through the existing global accounting path. Case3C is the producer-specific ownership record.

### 12.10 Completion

Case3C completes when:

```go
RecoveryRemainingUSD <= tolerance
```

On completion:

- remove the Case3C obligation;
- remove its retry record;
- clear the destination lot's recovery obligation ID and recovery balance;
- preserve ordinary AITransition operation.

The chain does not end merely because one rollover occurred.

## 13. Resource reservation

Add a separate reservation kind:

```go
ResourceReservationCase3C ResourceReservationKind = "case3c"
```

State-derived ledger rebuild must include:

```text
reservation ID prefix: case3c:
owner: obligation ID
producer: AITransitionTrader
side: obligation side
```

Reuse the existing Case3 reservation calculation:

- BUY obligation reserves quote using remaining base, target/live price, and fee multiplier.
- SELL obligation reserves base when the venue/configuration requires existing base.
- an active exchange order owns the resources, so the durable obligation reservation becomes informational to avoid double reservation.
- reconciliation state remains quarantined and must not release resources optimistically.

Ordinary producers must not consume funds reserved for a waiting Case3C obligation.

## 14. Persistence and restart

Persist:

- AITransition initialization state;
- previous raw AI;
- AITransition lots and rollover flags;
- pending entries and exits;
- Case3C obligations and retries;
- resource ledger;
- Producer Monitor attempts/economics through its existing persistence file.

At startup:

1. initialize missing Case3C maps;
2. reattach pending entry runtime callbacks;
3. restore Case3 commit eligibility only when the AI pending intent is a recovery augmentation;
4. reconcile exchange order state before retrying;
5. rebuild derived resource reservations;
6. never create a duplicate attempt from an already-known entry order ID.

Do not treat every restored AITransition pending entry as Case3C. Inspect `RecoveryAugmentation`.

## 15. Full-system reset

The full-system reset must clear:

```go
Case3CObligations = make(map[string]*Case3AObligation)
PendingCase3CRetries = make(map[string]PendingReplacementRetry)
```

It must also clear AITransition lots, pending lifecycle state, counters, latches, and producer attempts according to the reset contract.

Model weights and their fitted metadata are separate learned state and should remain preserved if that is the repository's established reset policy.

After reset and 50/50 rebalance, the next directional raw AI may create a fresh AITransition seed.

## 16. Concurrency and tick-boundary rules

The strategy must respect the repository's existing cycle lock and tick boundary.

- Full reset interrupts at the beginning of a tick, not in the middle of `step()`.
- Exit/recovery actions retain exit-first ownership of a tick.
- Producer evaluation may be parallel, but state mutation and commit remain serialized/atomic through existing locks.
- Never hold the trader mutex while performing an operation whose existing implementation reacquires it.
- After unlocking for exchange I/O, re-find lots by immutable entry order ID before mutating state.
- Never trust a previously captured slice index after the mutex was released.

## 17. Observability requirements

### 17.1 Reasons

Every AITransition reason must include regime.

Required prefixes:

```text
ai_transition_seed
ai_transition_rollover
ai_transition_rollover_fill
ai_transition_rollover_entry
case3C_decision
case3C_replacement
```

### 17.2 Producer Monitor

The monitor should show:

- the seed as one producer attempt;
- each confirmed rollover destination as another attempt;
- source attempt realized PnL and exit order ID;
- destination attempt entry order ID matching the source exit order ID;
- Case3C decision/submission/fill/consolidation stages on the AITransition producer card;
- obligation ID in Case3C stage reasons.

### 17.3 Transaction reconstruction

Given an exchange order ID, an operator must be able to reconstruct:

```text
source lot -> source exit record -> rollover destination attempt -> destination lot
```

The shared correlation key is the exit/destination entry order ID.

## 18. Files normally affected

Adapt filenames to the target repository:

| Concern | Typical file |
| --- | --- |
| Broker result and net-base helper | `broker.go` |
| Binance response parsing | `broker_binance.go` |
| Binance fill payload | `bridge_binance/ws_binance.py` |
| Trader state, rollover accounting, Case3C routing | `trader.go` |
| Tick scan, regime protection, transition authorization | `step.go` |
| Producer Monitor attempt linkage | `observability.go` |
| Reservation kinds and ledger replacement | `resource_manager.go` |
| Reset clearing | `reset.go` |
| Acceptance tests | dedicated `*_test.go` files |

Do not assume all files must be changed if `coinbase-v2` already centralizes some of these responsibilities.

## 19. Recommended implementation sequence for coinbase-v2

1. Locate and map the existing producer registry, entry pipeline, exit pipeline, pending-order lifecycle, Case3 obligation implementation, resource ledger, persistence, and Producer Monitor.
2. Add/confirm `AITransitionTrader` producer identity and durable lot fields.
3. Implement seed entry through the existing producer coordinator.
4. Add regime to every reason.
5. Implement raw-AI transition detection with both `FLAT -> direction` and direct reversal.
6. Add NORMAL profit-protected deferral using the existing profit-gate helper.
7. Implement confirmed-fill rollover and partial-fill continuation.
8. Add canonical linked rollover attempts to Producer Monitor.
9. Carry actual commission asset from bridge to broker result.
10. Net BUY base ownership exactly once and aggregate partial commissions by delta.
11. Generalize existing Case3 obligation helpers with intent-aware predicates.
12. Add Case3C NORMAL loss trigger before AI rollover selection.
13. Consolidate confirmed Case3C augmentation fills into the source lot.
14. Add Case3C resource reservation and restart reattachment.
15. Transfer Case3C obligation across rollovers and apply realized PnL without double counting.
16. Clear Case3C state during full reset.
17. Run focused tests, full tests, race tests where practical, and a live dry-run/reconciliation inspection before enabling capital.

## 20. Acceptance tests

### 20.1 Transition tests

- Fresh state + raw BUY creates one BUY seed.
- Fresh state + raw SELL creates one SELL seed.
- Existing BUY + raw SELL authorizes rollover.
- Existing SELL + raw BUY authorizes rollover.
- `FLAT -> BUY/SELL` remains valid.
- Repeated unchanged raw direction does not create duplicate transitions.

### 20.2 NORMAL protection tests

- Losing or insufficient-profit NORMAL lot defers rollover.
- Deferred transition persists while raw AI supports it.
- Unsubmitted deferred transition cancels if raw AI withdraws support.
- Deferred rollover submits once fee-aware required price is reached.
- UP/DOWN transition does not inherit NORMAL profit protection.

### 20.3 Fill and commission tests

- BUY fill with base commission owns `grossBase - commissionBase`.
- SELL fill is not reduced by the BUY net-credit helper.
- Multiple cumulative poll responses aggregate base commission by delta.
- Reprocessing the same cumulative response does not deduct commission twice.
- Partial rollover moves only confirmed quantity.
- Source residual and destination owned quantity reconcile to exchange facts, allowing explicitly attributed fee/dust differences.

### 20.4 Producer Monitor tests

- Each confirmed rollover creates exactly one new attempt.
- Duplicate processing of the same destination entry order ID creates no second attempt.
- Rollover attempt contains decision, exchange accepted, filled, and committed stages.
- Source exit order ID equals destination entry order ID.
- Source attempt receives realized PnL and exited stage.

### 20.5 Case3C tests

- AITransition loss in NORMAL triggers same-side Case3C augmentation.
- Same loss in UP or DOWN does not trigger Case3C.
- Source lot is not closed when augmentation is submitted.
- Confirmed augmentation fill consolidates into source lot.
- Partial augmentation fill applies proportional recovery economics.
- Waiting obligation reserves funds against ordinary producers.
- Restart restores obligation, pending entry ownership, and reservation.
- AI reversal does not cancel a submitted augmentation.
- Obligation transfers to rollover destination.
- Realizing the originally previewed loss does not double count it.
- Excess realized loss increases the obligation.
- Realized profit reduces the obligation.
- Obligation is deleted only when remaining recovery is at or below tolerance.

### 20.6 Reset tests

- Full reset deletes Case3C obligations and retries.
- Full reset deletes AITransition lots and pending rollover state.
- Model weights remain preserved when required by reset policy.
- Fresh post-reset directional AI creates a new seed.

## 21. Validation commands

Adapt filenames as necessary:

```bash
gofmt -w \
  broker.go \
  broker_binance.go \
  trader.go \
  step.go \
  observability.go \
  resource_manager.go \
  reset.go \
  ai_transition_case3c_test.go

go test -v -run \
  'Test(NetCreditedBase|AggregatedPartialFills|AITransition|Case3C)'

go test ./...
go test -race ./...
go build ./...

python3 -m py_compile bridge_binance/ws_binance.py
```

## 22. Live verification checklist

Before enabling meaningful capital:

1. Confirm the deployed image contains the intended version.
2. Confirm no unknown open exchange orders.
3. Confirm persisted lots match real balances.
4. Observe one seed attempt in Producer Monitor.
5. Observe one rollover and verify source exit ID equals destination entry ID.
6. Compare gross exchange quantity, commission asset, commission amount, and local lot base.
7. Confirm a NORMAL losing AI lot creates one Case3C obligation and one reservation.
8. Confirm ordinary producers cannot consume reserved Case3C funds.
9. Confirm augmentation fill consolidates rather than creating an unrelated lot.
10. Restart the bot and confirm the obligation and correlation IDs survive.
11. Confirm profitable rollover PnL reduces the same obligation.
12. Confirm completed obligation disappears from state and resource ledger.

## 23. Reference implementation validation

The source implementation from which this specification was produced was validated on September 23, 2026 with:

```bash
gofmt -w \
  broker.go \
  broker_binance.go \
  trader.go \
  step.go \
  observability.go \
  resource_manager.go \
  reset.go \
  ai_transition_case3c_test.go

go test -v -run \
  'Test(NetCreditedBase|AggregatedPartialFills|Case3CRollover|AITransitionRolloverAttempt)'

go test ./...
go build ./...
python3 -m py_compile bridge_binance/ws_binance.py
```

Validated focused tests:

- `TestNetCreditedBaseChargesBuyCommissionOnce`
- `TestAggregatedPartialFillsPreserveBaseCommission`
- `TestCase3CRolloverDoesNotDoubleCountTriggerLoss`
- `TestAITransitionRolloverAttemptIsIdempotent`

All focused tests passed, the complete Go test suite passed, `go build ./...` passed, and the Binance Python bridge compiled successfully.

## 24. Non-goals and prohibited shortcuts

Do not:

- replace the existing producer/order pipeline with a special AITransition-only broker path;
- change the strategy to direct transitions only;
- remove `FLAT -> direction` activation;
- create destination lots before confirmed fills;
- use gross BUY quantity as owned inventory when commission was charged in base;
- infer commission asset;
- count cumulative fill commission repeatedly;
- treat every AITransition entry as Case3C;
- create Case3C outside NORMAL;
- cancel a submitted augmentation merely because raw AI reversed;
- close the Case3C source at the loss threshold;
- start a new obligation at every rollover;
- keep one ProducerAttempt alive forever across all rollovers;
- release uncertain exchange ownership without reconciliation;
- delete model weights during reset when the established policy preserves them.

## 25. Final definition

`AITransitionTrader` is a persistent raw-AI directional chain with confirmed-fill rollovers. Each rollover closes one producer attempt and creates a linked new attempt. NORMAL may defer rollover until fee-aware profit protection is satisfied. If the chain instead crosses the configured NORMAL loss threshold, Case3C uses the existing Case3 same-side augmentation machinery and carries one durable recovery obligation through subsequent rollovers until authoritative realized profits complete it.

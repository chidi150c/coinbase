# Version-201 characterization coverage

Authoritative commit: `3188e3d1bd1626a8efeb66aac0e0966d09f8a1e4`  
Authoritative bot version: `201`

## Locked in this checkpoint

| Contract | Characterization |
|---|---|
| Producer decision | Case16A supplied market snapshot passes with SELL, LOW tier, priority 98 |
| Pending single-flight | Existing Case16A SELL pending count prevents a duplicate decision |
| Allocation priority | Higher priority receives resources before lower priority |
| Equal-priority sharing | Constrained resources are divided proportionally |
| Refund allocation | Producer core allocations are resolved before Refund attachment |
| Admission | Case3B blocks MID/HIGH but not LOW; LongOnly blocks SELL |
| Entry policy | NormalLegacy resets regime; Equity does not reset producer state |
| Recovery resurrection | BUY/SELL targets include taker fee plus five-basis-point slippage |
| Entry transport ordering | Exchange acceptance is registered and persisted before the asynchronous poller reaches the broker |
| Exit transport ordering | Exit submission reservation and accepted pending exit are persisted before the asynchronous watcher reaches the broker |
| Exit fan-out | Independent exit submissions reach the broker concurrently rather than serially |
| Recovery Mode A ordering | The replacement SELL submission occurs before the losing source BUY exit |
| Recovery Mode B ordering | The losing source BUY exit occurs before the initial replacement SELL, and both submissions occur in one close operation |
| Recovery deferred Mode B | The retry is consumed and quarantined durably before submission, accepted orders become ready through pending registration, and terminal failure hands off to active or reconciliation state |
| Recovery retry precedence | A pending one-time Mode B retry prevents target-controlled resurrection from taking ownership early |
| Recovery partial fill | Recovery USD is apportioned by core fill, remaining base/USD survive, and initial versus resurrected attempts return to active versus target-wait state |
| Recovery reconciliation | Uncertain recovery is quarantined and cannot produce a resurrection decision |
| Recovery market route | Resurrection validates executable BBO against target plus taker fee/slippage, disables maker execution, and commits through the shared fill path |
| Recovery completion | A fully committed recovery removes both its durable obligation and any deferred retry |
| Refund lifecycle | Obligations derive the opposite service side, reserve the largest outstanding debt, preserve debt on release, reduce only on confirmed service, and delete at completion |
| Refund ordering | Refund sizing attaches only after all producer core allocations have been considered |
| Refund exit throttle | Exit insufficient-balance obligations persist a 30-second NextRetryAt and defer only that exit while the deadline is active |
| Repricing | Enable/count/drift/improvement/edge guards precede cancel-replace, accepted reprices update economic fields, and a canonical repriced event carries old/new identity |
| Partial fills and uncertainty | Entry polling retains accumulated fill fields and terminal lifecycle stages; quarantined reservations reconcile against exchange state before release |

## Required before v2 cutover

The following require deterministic broker, clock, persistence, and scheduler seams. They are not claimed as covered by the first checkpoint:

- Complete decision fixtures for every installed version-201 producer.
- Continuation reference advancement and BUY/SELL latch rebasing.
- Tick ordering and parallel producer fan-in.
- Concurrent independent exit and entry submission.
- Complete Binance request payload matrices across market, post-only, and Recovery routes.
- Complete dynamic exit partial-fill accumulation and cancel-replace execution matrices.
- Recovery reconciliation against actual exchange state and crash-boundary persistence failures.
- Position/runner mutation, exit accounting, producer economics, lifecycle events, and pruning.
- Startup reconstruction and crash recovery.

No uncovered contract may be inferred from a newly designed v2 implementation. Each must first receive a passing version-201 characterization fixture.

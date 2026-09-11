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

## Required before v2 cutover

The following require deterministic broker, clock, persistence, and scheduler seams. They are not claimed as covered by the first checkpoint:

- Complete decision fixtures for every installed version-201 producer.
- Continuation reference advancement and BUY/SELL latch rebasing.
- Tick ordering and parallel producer fan-in.
- Concurrent independent exit and entry submission.
- Complete Binance request payload matrices across market, post-only, and Recovery routes.
- Partial entry and exit fill accumulation.
- Repricing, cancellation, timeout uncertainty, and reconciliation.
- Refund obligation creation, reservation, servicing, retry throttling, and completion.
- Recovery one-time post-exit Mode B retry, partial recovery, reconciliation, target wait, market resurrection, and completion.
- Position/runner mutation, exit accounting, producer economics, lifecycle events, and pruning.
- Startup reconstruction and crash recovery.

No uncovered contract may be inferred from a newly designed v2 implementation. Each must first receive a passing version-201 characterization fixture.

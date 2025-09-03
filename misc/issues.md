
======================================================================
✅ Summary — what really matters for correctness of state over a reboot

Open lots (t.lots)

DailyPnL & DailyStart (for circuit breaker)

LastAdd (for pyramiding rules)

Circuit breaker “paused” status

Optional but valuable: model weights, trade history.
==================================================================

Keep ORDER_MIN_USD=5.00 for the bot (already set) to avoid tiny partials; $1 tests are fine for manual probes but will often partial.

=================================================================

Want me to tune a TP ladder (e.g., 1.0% / 1.4% / 1.8%) and a trailing stop so you catch spikes without guessing a single number?

============================================================
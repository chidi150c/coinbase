# Summary (what we agreed)

Entry orders (maker-first, async):

When ORDER_TYPE=limit + offsets/timeouts qualify, we place a post-only limit in a goroutine and don’t block the tick.

We drain results at the start of step(): on fill we commit the lot; on timeout/error we clear pending; (optional) market fallback happens only after a re-check of gates on a later tick (not immediately).

Duplicate opens are prevented while the async order is pending on that side.
Exits are unaffected (they still run first and can close lots on the same side).

State & safety:

Pending open keeps side, limit price, base size, quote, take, reason, deadlines, and uses a channel to signal fill/timeout.

The goroutine is cancellable via the same ctx used by runLive(...).

We snap limit price to PriceTick and size to BaseStep (from exchange filters set in runLive).

Spare inventory accounting includes the pending order (so we don’t over-commit on the opposite side).

Scope boundaries:

We’re not “faking” a filled lot while pending; we only commit on real fill, so no rollback complexity.

Exits still use current logic (partial-exit robustness and reduce-only flags can be added in a later phase).

Implementation plan (phased, with files)
Phase 1 — Core async maker-first entry (non-blocking)

Files: ./trader.go

Add pending-open types & channel

Add PendingOpen and OpenResult structs.

Extend Trader with:

pending *PendingOpen

pendingResultCh chan OpenResult (buffered)

pendingCtx context.Context, pendingCancel context.CancelFunc

Drain results at start of step()

At the top of step(...), non-blocking drain pendingResultCh.

On Filled: commit lot (same path as current successful open), update lastAdd*, Slack, saveState().

On Timeout/Error: clear pending, cancel context if set (no immediate fallback here).

Prevent duplicate opens while pending (add-only path)

In ADD path (after EXIT path), insert guard:

If pending != nil && pending.Side == side ⇒ return "OPEN-PENDING ...".

Exit logic remains first and still executes.

Launch async maker attempt

Where you currently do maker-first synchronously, replace with:

Compute limit price with LimitPriceOffsetBps.

Snap limitPx to PriceTick, compute baseAtLimit = floor(quote/limitPx, BaseStep).

Validate baseAtLimit*limitPx >= MinNotional.

Call startPendingOpen(ctx, ...) to spawn goroutine, then return "OPEN-PENDING ...".

Keep market path unchanged for market-first configs (we won’t hit it on maker-async).

Goroutine body

PlaceLimitPostOnly(...)

Poll GetOrder(...) every ~200ms until LimitTimeoutSec.

On fill → send OpenResult{Filled:true, Placed:...}.

On timeout/shutdown → CancelOrder(...), send OpenResult{Filled:false}.

Respect ctx cancellation from runLive.

Reserve pending in spare accounting

In step(...) where you tally reserved base/quote:

Include pending.BaseAtLimit for SELL (if RequireBaseForShort).

Include pending.Quote (× fee mult) for BUY.

Deliverable: Compiles, runs; logs show OPEN-PENDING lines; filled orders get committed asynchronously; exits still fire.

Phase 2 — Optional “reassured” market fallback

Files: ./trader.go

Add side recheck flags

pendingRecheckBuy, pendingRecheckSell booleans on Trader.

Set recheck flag on timeout/error

In the result drain, when not filled → set the corresponding recheck flag.

Gate market fallback

Before any market open on a side that recently had a pending timeout/error, require:

pendingRecheck{Side} == true and all your existing gates still pass on this new tick.

After placing market, reset pendingRecheck{Side} = false.

If you want no market fallback at all, just skip this step and keep limit-only behavior.

Deliverable: Market fallback occurs only after a clean re-validation on a later tick; or can be disabled entirely.

Phase 3 — Exit robustness (partial fills)

Files: ./trader.go

Partial exit fix

In closeLot(...), if placed.BaseSize < requestedBase - tol:

Shrink the lot (SizeBase -= filled), pro-rate EntryFee, record an ExitRecord, do not remove from book.Lots, do not shift RunnerID.

Only remove the lot when fully filled.

Reduce-only on exits (if broker supports)

Add optional flag/parameter in broker call to guarantee exposure cannot increase on close.

Deliverable: Accurate P/L/state with partial closes, safer exits.

Phase 4 — Maker quality & resilience

Files: ./trader.go

Smart reprice in goroutine

Instead of a single resting order, add cancel/re-place every 300–500ms with the current snapped price, until deadline.

Stop on fill, timeout, or ctx.Done().

Spread/volatility-aware offset

Make LimitPriceOffsetBps adaptive: base + k*(spread_bps + vol_bps) (you already have volRiskFactor).

Price-protection guard for market

Before market fallback, ensure slippage cap (estimate from book or last/mark); otherwise prefer IOC at protected price.

Deliverable: Higher maker fill rate, fewer bad market prints.

Phase 5 — Metrics & visibility

Files: ./trader.go

New metrics

pending_open_side{side} gauge (0/1)

Counters: orders_open_maker, orders_open_taker, orders_close_maker, orders_close_taker

Fill ratios per class; avg commission per class.

Log polish

Clear [PENDING], [FILL], [TIMEOUT] tags; side and ids.

Deliverable: Easy observability of the new flow and cost savings.

Phase 6 — Small touch in runtime loop

Files: ./live.go

Only a comment (already drafted): async entries are spawned from step(ctx, ...) and honor ctx for clean shutdown.

No functional change required since we already pass ctx into startPendingOpen(...).

Deliverable: Clean shutdown of any in-flight maker attempts.

If you want, I can now generate Phase 1 as a ready-to-apply patch (exact diffs) for ./trader.go and then follow with the others.
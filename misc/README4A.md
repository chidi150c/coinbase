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
=========================================================================================================================================

# Forensic playbook: Go trading bot stalls with pending order + halted SELL/EXIT

Identify the container

Auto-pick likely container, else list all:

export C=$(docker ps --format '{{.Names}}\t{{.Image}}' | awk 'tolower($0) ~ /binance|coinbase|bot/ {print $1; exit}')
[ -z "$C" ] && docker ps --format '{{.Names}}\t{{.Image}}' && echo "Set: export C=NAME"
echo "Using container: $C"


Establish key paths/vars

State file and quick facts:

export STATE=/opt/coinbase/state/bot_state.newbinance.json
stat -c '%y  %n' "$STATE"
jq -r '
  "BUY lots=\((.BookBuy.lots|length)) runner_id=\(.BookBuy.runner_id) | SELL lots=\((.BookSell.lots|length)) runner_id=\(.BookSell.runner_id)",
  "NextLotSeq=\(.NextLotSeq)",
  "PendingBuy.id=\((.PendingBuy.OrderID//"nil")) recheck=\(.PendingRecheckBuy)",
  "PendingSell.id=\((.PendingSell.OrderID//"nil")) recheck=\(.PendingRecheckSell)"
' "$STATE"


Confirm live heartbeat vs stall

Trading loop heartbeat (should scroll every tick):

docker logs -f --since=5m "$C" | grep -E 'TRACE step\.start| \[TICK\] |TRACE TARGET \[TICK\]'


SELL-side opens path + post-only lifecycle:

docker logs -f --since=10m "$C" | grep -E 'TRACE sell\.gate\.(pre|post)|postonly\.(place|reprice|timeout).*side=SELL|OPEN-PENDING side=SELL'


Exits (both sides) path:

docker logs -f --since=10m "$C" | grep -E 'exit\.classify|trailing_stop|^EXIT |tp\.filled'


Narrow to the incident window

Pick a tight range around the last postonly.place you see (±2–4 min):

START='YYYY-MM-DDTHH:MM:SSZ'
END='YYYY-MM-DDTHH:MM:SSZ'
docker logs --since="$START" --until="$END" "$C" > /tmp/bot.window.log


In-window timeline extraction

Show state saves (did pending IDs persist?):

grep -E '\[WARN\] saveState|TRACE state\.save' /tmp/bot.window.log


Show opens/results around the event:

grep -E 'postonly\.(place|reprice|timeout)|OPEN-PENDING|^EXIT |tp\.filled' /tmp/bot.window.log


Count reprices (should advance OrderID each time):

grep 'postonly.reprice' /tmp/bot.window.log | wc -l


Correlate with on-disk state (consistency check)

Inspect pending details & deadlines:

jq -r '
  "PendingBuy: id=\((.PendingBuy.OrderID//"nil")) created=\((.PendingBuy.CreatedAt//"nil")) deadline=\((.PendingBuy.Deadline//"nil"))",
  "PendingSell: id=\((.PendingSell.OrderID//"nil")) created=\((.PendingSell.CreatedAt//"nil")) deadline=\((.PendingSell.Deadline//"nil"))",
  "Recheck flags: buy=\(.PendingRecheckBuy) sell=\(.PendingRecheckSell)",
  "Books: BUY=\((.BookBuy.lots|length)) SELL=\((.BookSell.lots|length))"
' "$STATE"


Check file mtime (did saves stop?):

stat -c '%y  %n' "$STATE"


Look for selective stalls (SELL/EXIT frozen while ticks continue)

If ticks continue but SELL/EXIT greps are empty/stale, note a logic or lock-specific stall, not a global freeze.

Capture a goroutine dump (golden evidence)

Send SIGQUIT and extract stacks:

docker kill -s QUIT "$C"
docker logs --since=2m "$C" | sed -n '/^goroutine [0-9]\+ \[/{:a;n;/^goroutine [0-9]\+ \[/q;p;ba}' > /tmp/goroutines.txt
tail -n +1 /tmp/goroutines.txt


What to look for (flag any matches):

goroutine 1 or step worker blocked on sync.Mutex.Lock (e.g., main.(*Trader).SetEquityUSD / step.func*).

Long “minutes” annotations on a goroutine in sync.Mutex.Lock.

Any network/disk I/O calls (place/reprice/cancel/save) inside a locked section.

Diagnose the failure pattern

If: postonly.reprice shows new IDs in logs but Pending*.OrderID in state is still the old ID → pending reassign not persisted or blocked by a lock.

If: SELL/EXIT greps go quiet while ticks continue → likely a deadlock/lock convoy inside step around equity/state updates.

If: Goroutine dump shows SetEquityUSD and step.func4 holding/contending the same Trader mutex for many minutes → confirm deadlock/lock contention as root cause.

Prescribe fixes (to include in incident note)

Move all network/disk I/O out of critical sections; keep locks only for in-memory mutations.

Centralize state writes in a single manager goroutine (fan-in channel), or switch to RWMutex with short-lived Lock.

On every postonly.reprice, update in-memory pending struct + persist state immediately (fire-and-forget channel).

Add defer mu.Unlock() and panic-safe unlock/logging around each critical region.

Add watchdogs/metrics: time a lock is held, and warn if >100ms; emit a heartbeat for SELL/EXIT pipelines.

Artifacts to archive with the report

/tmp/bot.window.log, /tmp/goroutines.txt, stat output for $STATE, and jq snapshots before/after.

Use this playbook to (a) reproduce the timeline, (b) prove the stall is a mutex deadlock/lock convoy, and (c) tie the stale pending OrderID to state-save under lock contention—then propose the lock/IO refactor.
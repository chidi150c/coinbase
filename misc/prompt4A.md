CONTINUATION CONTEXT — BINANCE BTCUSDT GO BOT ENTRY REFACTOR
Date: 2026-07-23

We are actively refactoring the Go trading bot. The priority is FAST IMPLEMENTATION and getting back to a compiling production build. Do not restart architectural brainstorming or propose endless new abstractions. Preserve behavior unless explicitly asked to change it.

============================================================
1. MAIN REFACTOR GOAL
============================================================

We are replacing the old fragmented entry lifecycle:

    pendingBuy  *PendingOpen
    pendingSell *PendingOpen
    pendingCase3A map[string]*PendingEntry

with ONE unified registry:

    pendingEntries map[string]*PendingEntry

The map key is the exchange OrderID.

PendingOpen is being REMOVED.

PendingIntent = entry decision/order data.
PendingEntry = live/runtime asynchronous entry lifecycle.

Architecture:

Decision Engine
      ↓
source-specific wrapper
      ↓
PendingIntent
      ↓
generic produceEntry()
      ↓
validatePendingIntent()
      ↓
submitPendingIntent()
      ↓
buildPendingEntry()
      ↓
registerPendingEntry()
      ↓
save state
      ↓
startEntryPoller()
      ↓
Exchange / repricing
      ↓
OpenResult
      ↓
ResultC
      ↓
generic drainPendingEntry()
      ↓
commitEntryFill()
      ↓
Position


============================================================
2. IMPORTANT STRUCTURE DECISIONS
============================================================

PendingEntry is generic.

Current direction is approximately:

    type PendingEntry struct {
        Side    OrderSide
        Source  EntryProducer
        OrderID string

        Intent *PendingIntent

        ResultC <-chan OpenResult
        Cancel  context.CancelFunc

        Book           *SideBook
        PendingRecheck *bool
        SpareUSD       *float64
        LastAdd        *time.Time
        WinExtreme     *float64
        LatchedGate    *float64

        EquityTriggered bool

        SourceEntryOrderID string
        Completed          bool

        CommitEligible func(*PendingEntry) bool

        clearOwner func()
    }

IMPORTANT:
- Field is `Intent`, capital I. NOT `intent`.
- Legacy `entry.Pending` is being removed/replaced by `entry.Intent`.
- Do NOT reintroduce PendingOpen.
- OrderID is generic and is also the pendingEntries map key.
- SourceEntryOrderID is different: for Case3A it identifies the source position being recovered.


============================================================
3. PENDING INTENT
============================================================

PendingIntent contains the actual entry/order metadata such as:

    Side
    Source
    LimitPx
    BaseAtLimit
    Quote
    Take
    Reason
    RefundPortionUSD
    ProductID
    CreatedAt
    Deadline
    EquityBuy
    EquitySell
    OrderID
    History
    AccumBase
    AccumQuote
    AccumFeeUSD
    ConfidenceMult
    ProfitGateUSD
    EntryMethod
    CancelRequested

Do NOT put function callbacks such as CommitEligible inside PendingIntent.
PendingIntent is data/persistable state.
PendingEntry owns runtime behavior/callbacks.


============================================================
4. GENERIC ENTRY PRODUCER
============================================================

The generic producer now owns:

    validate intent
        ↓
    submit maker order
        ↓
    create PendingEntry
        ↓
    register pendingEntries[OrderID]
        ↓
    persist state
        ↓
    start poll/reprice goroutine
        ↓
    return PendingEntry

Source wrappers construct PendingIntent and call produceEntry().

Normal BUY/SELL and Case3A should all eventually use this producer.


============================================================
5. REGISTRY / CONCURRENCY
============================================================

pendingEntries is the single source of truth.

Registry mutation should be centralized through:

    registerPendingEntry()
    rekeyPendingEntry()
    cleanup/removal via clearOwner or equivalent

registerPendingEntry() was updated to lock t.mu around map initialization/check/insertion.

Repricing:
- Do NOT rekey twice.
- We decided the poller should have one clear owner for rekey behavior.
- Preserve OrderID/History behavior across reprices.

Poller context:
- startEntryPoller(ctx, entry) owns creation of the child poller context.
- buildPendingEntry() should not create a second unused context.


============================================================
6. GENERIC DRAIN
============================================================

step() should no longer drain BUY, SELL and Case3A separately.

We added:

    func (t *Trader) pendingEntriesSnapshot() []*PendingEntry {
        t.mu.Lock()
        defer t.mu.Unlock()

        entries := make([]*PendingEntry, 0, len(t.pendingEntries))
        for _, entry := range t.pendingEntries {
            if entry != nil {
                entries = append(entries, entry)
            }
        }

        return entries
    }

step() uses:

    entries := t.pendingEntriesSnapshot()

    for _, entry := range entries {
        t.drainPendingEntry(
            entry,
            now,
            wallNow,
        )
    }

The snapshot is intentional:
- registry is copied under lock;
- lock is released;
- drain can remove entries safely.

Do NOT replace this with iteration while holding t.mu.


============================================================
7. COMMIT ELIGIBILITY / Case3A
============================================================

Old special function:

    drainPendingCase3AEntries()

is being DELETED.

The unique Case3A behavior was:

Do not consume/commit the replacement fill until the originating loss exit has committed.

That behavior is now attached to PendingEntry:

    CommitEligible func(*PendingEntry) bool

At top of drainPendingEntry():

    if entry == nil ||
        entry.Completed ||
        entry.ResultC == nil {
        return
    }

    if entry.CommitEligible != nil &&
        !entry.CommitEligible(entry) {
        return
    }

Normal BUY/SELL:
    CommitEligible == nil

Case3A:
    CommitEligible = t.Case3ACommitEligible

Current eligibility function:

    func (t *Trader) Case3ACommitEligible(
        entry *PendingEntry,
    ) bool {
        if t == nil || entry == nil {
            return false
        }

        if entry.Source != EntrySourceCase3A {
            return true
        }

        sourceEntryOrderID :=
            strings.TrimSpace(entry.SourceEntryOrderID)

        if sourceEntryOrderID == "" {
            return false
        }

        return !t.positionExistsByEntryOrderID(
            sourceEntryOrderID,
        )
    }

DO NOT refactor this further right now.

The assignment should happen when generic buildPendingEntry() creates a Case3A PendingEntry:

    if intent.Source == EntrySourceCase3A {
        entry.CommitEligible = t.Case3ACommitEligible
    }

Normal entries naturally leave the callback nil.

Do not put the function in PendingIntent.


============================================================
8. ENTRY POLICY / CASE 10
============================================================

We introduced source-based EntryPolicy:

    type EntryPolicy struct {
        ResetLastAdd
        ResetWinExtreme
        ResetLatchedGate
        AllowRunner
        UpdateEquityBaseline
        ResetRegime
    }

Normal entries:
    ResetRegime = true

Case3A:
    ResetRegime = false

Case 10 behavior:

A successful NORMAL entry resets directional regime only when entry agrees with current regime:

    UP   + BUY  -> NORMAL
    DOWN + SELL -> NORMAL

Case3A replacement DOES NOT reset regime.

Reason:
A DOWN regime + successful SELL means the current directional opportunity was consumed.
If the market continues making fresh lower lows, normal regime detection can switch it DOWN again.
Otherwise the downtrend may be exhausted and NORMAL should persist.

Current helpers:

    func (t *Trader) toNormal(reason string)

    func (t *Trader) shouldResetRegime(side OrderSide) bool

Do not reopen Case 10 design right now.


============================================================
9. RESERVATION ACCOUNTING IN step()
============================================================

Old code used:

    t.pendingSell
    t.pendingBuy

Those fields are gone.

Behavior MUST remain:

- BUY live lots reserve base.
- pending SELL reserves base when RequireBaseForShort.
- SELL live lots reserve quote + fee.
- pending BUY reserves quote + fee.

Use pendingEntriesSnapshot() and entry.Intent.

Example direction:

    pendingEntries := t.pendingEntriesSnapshot()

    for _, entry := range pendingEntries {
        if entry == nil ||
            entry.Completed ||
            entry.Intent == nil {
            continue
        }

        intent := entry.Intent

        switch entry.Side {
        case SideSell:
            if t.cfg.RequireBaseForShort {
                reservedLongBase += intent.BaseAtLimit
            }

        case SideBuy:
            reservedShortQuoteWithFee +=
                intent.Quote * feeMult
        }
    }

IMPORTANT:
Use `entry.Intent`, NOT `entry.intent`.
Use `entry.Intent`, NOT legacy `entry.Pending`.


============================================================
10. CURRENT BUILD ERRORS
============================================================

Latest:

    go build .

produced:

    ./trader.go:1875:21: undefined: ReplacementRequest
    ./trader.go:1881:56: undefined: ReplacementRequest
    ./trader.go:3639:7: undefined: ReplacementRequest

    ./step.go:1017:9:
        entry.intent undefined
        type *PendingEntry has field Intent

    ./step.go:1021:19:
        entry.Pending undefined

    ./step.go:1441...:
        t.pendingBuy undefined

    ./step.go:1455...:
        t.pendingSell undefined

    ...more errors hidden after "too many errors"

Immediate fixes:
- entry.intent -> entry.Intent
- entry.Pending -> entry.Intent where this is legacy PendingOpen metadata.
- remaining pendingBuy/pendingSell code must migrate to pendingEntries.
- ReplacementRequest needs to be resolved next.


============================================================
11. REPLACEMENTREQUEST / Case3A — WHERE WE STOPPED
============================================================

We were just starting to fix the Case3A retry flow.

The uploaded closeLot() still uses ReplacementRequest heavily.

Current Case3A recovery behavior in closeLot():

SELL threshold_stop_loss with actual loss.

Mode A:
- sufficient spare base;
- allowed in ANY regime;
- replacement base = normalBase + extraBase;
- method RecoveryByPositionSize;
- replacement should start before loss exit.

Mode B CURRENT FILE:
- insufficient spare;
- only RegimeDown;
- normalBase;
- method RecoveryByProfitTarget;
- ProfitGateUSD = cfg.ProfitGateUSD + recoveryNetUSD.

The current closeLot file explicitly contains this behavior. Do not accidentally change it while fixing compile errors.

Mode A flow:
    startCase3AReplacement(ctx, repl)
    BEFORE loss exit.
    If replacement fails, abort loss exit.

Mode B maker-exit flow:
    start loss exit
    then startCase3AReplacement(ctx, repl)
    if replacement fails:
        markCase3AReplacementRetryLocked(...)

Mode B market-exit flow:
    market exit accepted
    then startCase3AReplacement(ctx, repl)
    if replacement fails:
        markCase3AReplacementRetryLocked(...)

The file still declares:

    var repl ReplacementRequest

and constructs ReplacementRequest values.

So the current undefined ReplacementRequest build errors must be resolved without losing this behavior.

IMPORTANT HISTORICAL REQUIREMENT:
There was also a newer desired Case3A Mode B rule discussed:

    Insufficient spare base
    + UP regime
    + MACD strong
    + (EMA high peak || EMA up-down)
    -> Mode B replacement

But the currently uploaded closeLot() still implements Mode B only in DOWN.
Do NOT silently implement the newer UP/MACD/EMA rule while merely fixing compilation.
That should be handled deliberately afterward.


============================================================
12. CURRENT RETRY BLOCK
============================================================

We had just pasted this retry block and were about to refactor it:

    if t.PendingReplacementRetry.Enabled {
        repl := t.PendingReplacementRetry.Replacement

        OrderID, err := t.startCase3AReplacement(
            ctx,
            repl,
        )
        if err != nil {
            log.Printf(
                "[TRACE] Case3A.retry.failed method=%s err=%v",
                repl.Method.String(),
                err,
            )
        } else {
            log.Printf(
                "[TRACE] Case3A.retry.started method=%s replacement_order_id=%s",
                repl.Method.String(),
                OrderID,
            )

            t.PendingReplacementRetry.Enabled = false

            if err := t.saveStateNoLock(); err != nil {
                log.Printf(
                    "[TRACE] Case3A.retry.state_save_failed replacement_order_id=%s err=%v",
                    OrderID,
                    err,
                )
            }
        }
    }

THIS IS WHERE IMPLEMENTATION SHOULD RESUME.

Need determine how PendingReplacementRetry.Replacement should evolve now that ReplacementRequest is undefined and Case3A ultimately feeds the generic PendingIntent/PendingEntry producer.

Do not invent a new architecture before inspecting the current relevant structs/functions.


============================================================
13. IMPORTANT: DO NOT REINTRODUCE OLD FIELDS
============================================================

Do NOT restore:

    pendingBuy *PendingOpen
    pendingSell *PendingOpen
    pendingCase3A map[string]*PendingEntry

Do NOT restore PendingOpen merely to make compilation pass.

The goal is to finish migration to:

    pendingEntries map[string]*PendingEntry
    PendingIntent
    PendingEntry


============================================================
14. STEP() ENTRY PRODUCTION
============================================================

The old huge inline maker-first entry production in step() is being replaced by source wrappers + generic producer.

Intended normal flow:

    step()
      ↓
    startProducerBuyEntry()/startProducerSellEntry()
      ↓
    PendingIntent
      ↓
    produceEntry()

Do not recreate inline:
- PlaceLimitPostOnly
- PendingOpen
- custom goroutine
- side-specific pending channels


============================================================
15. WORKING STYLE REQUIRED
============================================================

User wants implementation instructions in this form:

    "Find this function signature:
        func (...)

     Replace the entire function with:
        ..."

or:

    "Find this exact block:
        ...

     Replace it with:
        ..."

Do NOT give vague architecture-only advice when code can be supplied.

Do NOT keep suggesting further abstractions after the requested fix works.

Do NOT change trading behavior unless explicitly requested.

For each compiler error:
1. identify the old architecture assumption;
2. give exact replacement;
3. preserve existing behavior;
4. rerun `go build .`;
5. handle the next compiler errors.

NEXT TASK:
Continue from the Case3A retry flow / undefined ReplacementRequest errors.
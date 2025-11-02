

============================================================

FORCE_FILTERS_REMOTE=1 — This will force the broker to call the bridge HTTP endpoint /exchange/filters. Your current Coinbase sidecar (app.py) doesn’t expose that route, so the fetch will fail and your bot is likely to abort at boot. Either remove this for Coinbase or add /exchange/filters to the bridge.

=============================

Stage index lifecycle. You increment equityStage{Buy|Sell} after a chosen stage, but index resets can happen after restart, side change, or other state transitions; also the next trigger can still pick another large fraction if spare didn’t shrink.

=====================================

Short answer: sometimes.

Maker-first OPENs (post-only) → Yes, consolidated.
Your open poller accumulates per-order deltas into session totals (sessBase/sessQuote/sessFee) across partial fills and reprices, then emits a single VWAP’d PlacedOrder. So multiple partials become one consolidated fill for the append.

Maker-first EXITs (post-only) in closeLot → No, not consolidated (currently).
You accept the first observed fill (ord.BaseSize>0 || ord.QuoteSpent>0), treat it as the execution, and break. If that’s only a partial, you do a partial exit (shrink the lot) and return. You don’t keep polling to accumulate more partials into a consolidated VWAP like you do on open.

Market EXITs/OPENs → Usually a single broker call; if the broker returns a partial, you use the actual filled size and treat it as a partial (lot is reduced on exit; on open you append the actually filled size).

“Dust” consolidation (consolidateDust) → Only runs after appends (you call it when adding a lot, including async BUY/SELL drains and market/limit opens). It does not run after a partial exit, so a tiny leftover lot isn’t merged then. Also, the implementation currently only does “newest dust → merge backward”; it doesn’t sweep older dust forward into the newest lot.

If you want parity with OPENs, the upgrade would be to make the maker-first EXIT loop accumulate sessBase/sessQuote/sessFee until filled/timeout (like the open poller) and then close that consolidated amount in one go.

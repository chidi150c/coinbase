

============================================================

FORCE_FILTERS_REMOTE=1 — This will force the broker to call the bridge HTTP endpoint /exchange/filters. Your current Coinbase sidecar (app.py) doesn’t expose that route, so the fetch will fail and your bot is likely to abort at boot. Either remove this for Coinbase or add /exchange/filters to the bridge.

=============================

Stage index lifecycle. You increment equityStage{Buy|Sell} after a chosen stage, but index resets can happen after restart, side change, or other state transitions; also the next trigger can still pick another large fraction if spare didn’t shrink.

=====================================

Recheck flag & market fallback semantics

You’re right to call this out. Two clarifications:

Bug observed: after timeouts the bot kept placing new maker orders every minute (log showed postonly.place → timeout → recheck=true → place again). That’s a leak even without market fallback.

Your desired behavior: sometimes you want limit-only (never market). Other times you want a single fallback to market after a failed maker attempt.

Make it explicit with a knob:

AllowMarketFallback (bool)

If true: On pendingRecheck*==true, skip maker once and place a single market open, then reset the flag.

If false (limit-only mode): On recheck, do not market; do not re-place maker immediately; just HOLD and clear the flag (or wait out a cooldown). This avoids the 60s maker churn.
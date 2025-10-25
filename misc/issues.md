

============================================================

FORCE_FILTERS_REMOTE=1 — This will force the broker to call the bridge HTTP endpoint /exchange/filters. Your current Coinbase sidecar (app.py) doesn’t expose that route, so the fetch will fail and your bot is likely to abort at boot. Either remove this for Coinbase or add /exchange/filters to the bridge.

=============================

Stage index lifecycle. You increment equityStage{Buy|Sell} after a chosen stage, but index resets can happen after restart, side change, or other state transitions; also the next trigger can still pick another large fraction if spare didn’t shrink.

=====================================

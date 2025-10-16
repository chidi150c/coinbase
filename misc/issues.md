

============================================================

FORCE_FILTERS_REMOTE=1 — This will force the broker to call the bridge HTTP endpoint /exchange/filters. Your current Coinbase sidecar (app.py) doesn’t expose that route, so the fetch will fail and your bot is likely to abort at boot. Either remove this for Coinbase or add /exchange/filters to the bridge.
# Optional 30-feature AI shadow on version201

This patch adds a **separate, disabled-by-default** BTCUSDT 1-minute model alongside version201. Version201 still uses its existing 5-minute `pUp` model, thresholds, risk sizing, producer logic, and order path. The new model cannot submit orders or modify Trader state. The three-class 30-feature model was trained on 180 days of completed 1-minute BTCUSDT candles and has validation-selected BUY/SELL confidence gates. Its six cluster flags are derived from a frozen training-only cluster scaler and centers.

The live loop copies execution candles and sends them to a bounded background worker. The worker makes a private copy of the last candle's close/high/low using the current tick price, computes the 24 engineered features and six one-hot flags, applies the model and gates, and logs `[AI_SHADOW] model=... decision_id=... tick=... candle=... group=... raw=... confidence=... p_buy=... p_sell=... p_flat=... no_orders=true`. The worker may drop a snapshot if busy. It cannot delay `step()` through inference or broker calls. Version201's existing one-candle resync can skip minutes, so the shadow worker alone fetches a short 1-minute batch to repair its own history when it detects a gap or stale candle. This fetch has a two-second timeout and is attempted no more than once per 15 seconds; predictions are skipped until history is continuous and current. No additional fetch occurs on the trading tick. The model is intentionally not persisted inside version201's old binary-model state or included in walk-forward refits.

The running bot reads persistent artifacts under `/opt/coinbase/state/`. Copy the two files from your checkout to that state directory (on the host), with permissions readable by the bot container:

```sh
sudo install -d -m 0755 /opt/coinbase/state/ai-shadow30
sudo install -m 0644 shadow_models/AI30ClusterOneMinute180dLBFGS.json /opt/coinbase/state/ai-shadow30/
sudo install -m 0644 shadow_models/btc_24feature_six_groups.json /opt/coinbase/state/ai-shadow30/
```

Enable only when running `PRODUCT_ID=BTCUSDT` and `GATE_TF=ONE_MINUTE`. Supply both **container-visible** artifact paths; leaving both unset disables it:

```sh
AI_SHADOW_30_MODEL=/opt/coinbase/state/ai-shadow30/AI30ClusterOneMinute180dLBFGS.json
AI_SHADOW_30_CLUSTER=/opt/coinbase/state/ai-shadow30/btc_24feature_six_groups.json
```

These paths work when the host state directory is mounted at the same `/opt/coinbase/state/` path inside the bot container. For a local process running from the repository root, you may use `./shadow_models/...` instead. The Docker image does not embed model artifacts. A missing/invalid artifact or a different product/timeframe disables only shadow inference and logs the reason; the existing model keeps running. Set both variables back to empty to disable the shadow worker on the next process start. Shadow predictions do **not** promote this model or claim executed profitability.

Before any deployment, run:

```sh
gofmt -w ai_shadow_30.go ai_shadow_indicators.go ai_shadow_30_test.go live.go
go test ./...
go build ./...
```

`TestShadow30ParityWithFrozenTrainingFeatures` checks the feature sequence, cluster ID, three class probabilities, and confidence against a fixture computed using the original training math and model. The 120-candle fixture verifies the private last-candle tick mutation. Full production-history warmup, actual fees, and real executions remain outside that fixture. This execution environment lacked Go, so these commands must run in your checkout before enabling shadow evaluation. Compare shadow logs to version201's decisions on subsequent data; the reused offline test interval is no longer untouched. Promoting this three-class model to the old producer is a separate change and must not reinterpret its class confidence as version201's binary `pUp`.

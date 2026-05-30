# FeatureDim=17 Hybrid Merge Notes

Generated from the diff between `4a92587` and current HEAD.

## Goal

Restore the useful EMA structure that was removed by the MACD-compression experiment, while keeping the later engineering fixes:

- walk-forward model persistence
- invalid-fit state protection
- refund-service spare accounting
- causal MACD slope helper
- improved logging

## Feature vector

`UnifiedFeatureDim = 17`

```text
0  ret1
1  ret5
2  RSI14 / 100
3  ZScore20
4  ATR14 / Close
5  realizedVol20 / Close
6  distance from recent high, pct
7  distance from recent low, pct
8  MACD line / Close
9  MACD histogram / Close
10 MACD d1 / Close
11 MACD d2 / Close
12 MACD d3 / Close
13 EMA4/EMA8 spread pct
14 EMA20/EMA50 spread pct
15 EMA20 slope
16 EMA50 slope
```

## Integration order

1. Replace `feature_builder.go` with the generated file.
2. Apply `featuredim17_hybrid_integration.patch`.
3. Run:

```bash
gofmt -w feature_builder.go indicators.go model.go strategy.go config.go env.go live.go step.go trader.go
go test ./...
```

4. Reset model state to 17:

```bash
jq '.Model.W=null | .Model.B=0 | .Model.FeatDim=17 | .LastFit="0001-01-01T00:00:00Z"' \
/opt/coinbase/state/bot_state.newbinance.json \
> /tmp/state_reset.json && \
sudo mv -f /tmp/state_reset.json /opt/coinbase/state/bot_state.newbinance.json
```

5. Archive old labels and re-mine:

```bash
sudo mv /opt/coinbase/state/mined_labels_binance_5m.jsonl \
/opt/coinbase/state/mined_labels_binance_5m.jsonl.bak.$(date +%Y%m%d_%H%M%S)
```

Then after image rebuild/deploy:

```bash
cd ~/coinbase/monitoring
IMAGE_SHA=<new_sha> docker compose run --rm --no-deps --entrypoint /app/bot bot_binance -mine-tf 5m -limit 50000
```

6. Restart and verify:

```bash
docker compose logs --since "20m" bot_binance | grep -E 'DATASET|MINED_LABELS|MODEL_FIT|MODEL_STATE|feat_dim|rows='
jq '.Model.FeatDim, (.Model.W|length), .LastFit' /opt/coinbase/state/bot_state.newbinance.json
```

Expected:

```text
feat_dim=17
weights=17
MINED_LABELS loaded rows > 0
MODEL_FIT rows > 0
```

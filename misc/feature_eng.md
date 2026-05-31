# Feature Engineering Change Runbook

This document explains the correct process for adding, removing, or changing AI model features in the trading bot.

## 1. Core rule

Any feature change must keep these items synchronized:

```text
feature_builder.go
↓
UnifiedFeatureDim
↓
FeatureSnapshot fields
↓
decision/debug logs
↓
model state dimension
↓
mined label feature vectors
↓
trained model weights
```

If any one of these is out of sync, the bot may train zero rows, save invalid weights, or keep using an old model.

---

## 2. Update `feature_builder.go`

When adding/removing features, update the feature vector:

```go
x := []float64{
    ...
}
```

Then count the exact number of values in `x`.

Example:

```go
x := []float64{
    ret1,                      // 0
    ret5,                      // 1
    safeRatio(rsis[idx],100),  // 2
    zs[idx],                   // 3
}
```

Feature count is `4`.

---

## 3. Update `UnifiedFeatureDim`

The constant must match `len(x)` exactly.

```go
const UnifiedFeatureDim = 14
```

If this is wrong, this check will reject all rows:

```go
if len(x) != UnifiedFeatureDim || hasBadFloat(x) {
    return out, false
}
```

Symptom of mismatch:

```text
[DATASET] labeled=0 bad=...
feat_dim=0
[WARN] dataset rows too small
```

---

## 4. Update `FeatureSnapshot`

`FeatureSnapshot` should only contain fields still used by logs, gates, or decision reasons.

If a feature is removed from `x`, remove unused snapshot fields too, unless another part of the code still needs them.

Example:

```go
type FeatureSnapshot struct {
    X []float64

    HighPeak  bool
    LowBottom bool

    MACDLine float64
    MACDHist float64
    MACDD1   float64
    MACDD2   float64
    MACDD3   float64

    DistHighPct float64
    DistLowPct  float64
}
```

---

## 5. Update `out = FeatureSnapshot{}`

Every retained snapshot field must be assigned.

Example:

```go
out = FeatureSnapshot{
    X:           x,
    HighPeak:    highPeak,
    LowBottom:   lowBottom,
    MACDLine:    macdLineNow,
    MACDHist:    histNow,
    MACDD1:      d1,
    MACDD2:      d2,
    MACDD3:      d3,
    DistHighPct: distHighPct,
    DistLowPct:  distLowPct,
}
```
## 6. Update reason
```go
reason := fmt.Sprintf(
    "pUp=%.5f, highPeak_%s=%t, lowBottom_%s=%t, distHighPct_%s=%.6f, distLowPct_%s=%.6f, macdLine_%s=%.5f, macdHist_%s=%.5f, macdD1_%s=%.5f, macdD2_%s=%.5f, macdD3_%s=%.5f",
    pUp,
    signalTF, snap.HighPeak,
    signalTF, snap.LowBottom,
    signalTF, snap.DistHighPct,
    signalTF, snap.DistLowPct,
    signalTF, snap.MACDLine,
    signalTF, snap.MACDHist,
    signalTF, snap.MACDD1,
    signalTF, snap.MACDD2,
    signalTF, snap.MACDD3,
)
```

---

## 7. Update model creation

`newModel()` must create a model using the current feature dimension:

```go
func newModel() *LogisticModel {
    return NewLogisticModel(UnifiedFeatureDim)
}
```

Do not hardcode an old dimension inside `newModel()`.

---

## 8. Run code checks

After edits:

```bash
cd ~/coinbase

gofmt -w feature_builder.go step.go model.go live.go
go test ./...
```

If build fails, search for old removed fields.

---

## 9. Reset model state

When feature dimension changes, old weights are invalid.

Reset model state:

```bash
jq '.Model.W=null | .Model.B=0 | .Model.FeatDim=14 | .LastFit="0001-01-01T00:00:00Z"' \
/opt/coinbase/state/bot_state.newbinance.json \
> /tmp/state_reset.json && \
sudo mv -f /tmp/state_reset.json /opt/coinbase/state/bot_state.newbinance.json
```

Change `14` to the new `UnifiedFeatureDim`.

Verify:

```bash
jq '.Model,.LastFit' /opt/coinbase/state/bot_state.newbinance.json
```

Expected:

```json
{
  "W": null,
  "B": 0,
  "L2": 0.001,
  "FeatDim": 14
}
"0001-01-01T00:00:00Z"
```

---

## 10. Re-mine labels after feature changes

Mined labels contain saved feature vectors. If feature shape changes, old mined labels are no longer compatible.

Remove or archive the old mined label file:

```bash
sudo mv /opt/coinbase/state/mined_labels_binance_5m.jsonl \
/opt/coinbase/state/mined_labels_binance_5m.jsonl.bak.$(date +%Y%m%d_%H%M%S)
```

Then re-mine:

```bash
cd ~/coinbase/monitoring

IMAGE_SHA=b821c8df26d732c2ccaf1565cdf688d860ed3981 \
docker compose run --rm --no-deps \
  --entrypoint /app/bot \
  bot_binance \
  -mine-tf 5m \
  -limit 50000
```

Verify label count:

```bash
wc -l /opt/coinbase/state/mined_labels_binance_5m.jsonl
grep '"y":1' /opt/coinbase/state/mined_labels_binance_5m.jsonl | wc -l
grep '"y":0' /opt/coinbase/state/mined_labels_binance_5m.jsonl | wc -l
```

---

## 11. Push and redeploy

Commit:

```bash
cd ~/coinbase

git status
git add feature_builder.go step.go model.go live.go
git commit -m "Update AI feature dimension and feature engineering"
git push
```

Redeploy using the normal image pipeline.

Restart service:

```bash
cd ~/coinbase/monitoring

docker compose restart bot_binance
```

---

## 12. Verify live training

Check logs:

```bash
docker compose logs --since "10m" bot_binance \
| grep -E 'DATASET|MINED_LABELS|MODEL_FIT|MODEL_STATE|feat_dim|rows='
```

Expected:

```text
[MINED_LABELS] loaded rows=...
[MODEL] trained rows=... feat_dim=14
[MODEL_FIT] rows=... feat_dim=14
[MODEL_STATE] saved ... feat_dim=14 weights=14
```

Verify state:

```bash
jq '.Model.FeatDim, (.Model.W|length), .LastFit' \
/opt/coinbase/state/bot_state.newbinance.json
```

Expected:

```text
14
14
"2026-..."
```

---

## 13. Common failure symptoms

### Symptom: dataset rows become zero

Logs:

```text
[DATASET] labeled=0 feat_dim=0
[WARN] dataset rows too small
```

Likely cause:

```text
len(x) != UnifiedFeatureDim
```

Fix:

```bash
grep -n "UnifiedFeatureDim" feature_builder.go
grep -n "x := \[\]float64" -A30 feature_builder.go
```

Count features manually and correct `UnifiedFeatureDim`.

---

### Symptom: model saves old dimension

State shows:

```text
FeatDim=20
weights=20
```

but new code expects:

```text
FeatDim=14
```

Likely causes:

```text
state was not reset
new image was not deployed
newModel() still creates old dimension
training failed and old model survived
```

---

### Symptom: no new labels used

Logs show:

```text
[MINED_LABELS] skipped rows=0
```

Likely causes:

```text
old label file incompatible
feature builder failing
wrong mined label path
```

Check:

```bash
ls -lh /opt/coinbase/state/*label*
wc -l /opt/coinbase/state/mined_labels_binance_5m.jsonl
```

---

## 14. Safe order of operations

Use this order every time:

```text
1. Edit feature_builder.go
2. Update UnifiedFeatureDim
3. Update FeatureSnapshot
4. Update logs/reason strings/gates
5. gofmt + go test
6. Reset model state
7. Archive old mined labels
8. Re-mine labels
9. Push/redeploy
10. Restart bot
11. Verify MODEL_FIT feat_dim and weights
```

Do not skip model reset or label re-mining after a dimension change.

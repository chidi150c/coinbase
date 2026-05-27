We are evaluating whether my Binance BTCUSDT AI model has actual predictive edge by confidence bucket before abandoning the project.

Context:
- Bot emits:
  Raw=BUY / Raw=SELL / Raw=FLAT
- Model probability:
  pUp
- Current thresholds:
  BUY_THRESHOLD=0.52
  SELL_THRESHOLD=0.47
- Confidence sizing logic:

func confidenceRiskMultiplier(sig Signal, pUp float64) float64 {
	if sig == Buy {
		switch {
		case pUp >= 0.62:
			return 1.00
		case pUp >= 0.58:
			return 0.85
		case pUp >= 0.55:
			return 0.65
		case pUp >= 0.52:
			return 0.40
		default:
			return 1.00
		}
	}

	if sig == Sell {
		switch {
		case pUp <= 0.38:
			return 1.00
		case pUp <= 0.42:
			return 0.85
		case pUp <= 0.45:
			return 0.65
		case pUp <= 0.47:
			return 0.40
		default:
			return 1.00
		}
	}

	return 1.00
}

Goal:
Determine whether higher-confidence signals have directional edge even if overall model performance looks poor.

Input file:
A bot log containing:
- [DEBUG] ... Raw=BUY/SELL/FLAT pUp=...
- [TICK] px=...

Required methodology:
1. Parse the log.
2. Ignore repeated identical signals (signal spam). Use transition-based signals only:
   - Only count a new signal when Raw changes:
     BUY→SELL
     SELL→BUY
     FLAT→BUY
     FLAT→SELL
3. Ignore FLAT as a signal.
4. For every BUY signal:
   - record entry timestamp
   - record entry price
   - compare future price:
       +30 minutes
       +60 minutes
   - BUY is correct if:
       future_price > entry_price
5. For every SELL signal:
   - record entry timestamp
   - record entry price
   - compare future price:
       +30 minutes
       +60 minutes
   - SELL is correct if:
       future_price < entry_price
6. Group results by confidence bucket.

BUY buckets:
- >=0.62
- 0.58–0.62
- 0.55–0.58
- 0.52–0.55

SELL buckets:
- <=0.38
- 0.38–0.42
- 0.42–0.45
- 0.45–0.47

For each bucket compute:
- signal count
- 30m accuracy %
- 60m accuracy %
- average move %
- median move %
- max favorable excursion %
- max adverse excursion %

Output format:
1. Summary table by bucket
2. Strongest buckets ranked
3. Weakest buckets ranked
4. Whether high-confidence buckets have statistically meaningful edge
5. Recommendation:
   - tighten thresholds?
   - disable SELL?
   - LONG_ONLY?
   - abandon current model?
6. Do NOT hand-wave. Use actual computed values only.

Important:
If overall model accuracy is poor but high-confidence buckets are good, conclude that thresholds should be tightened rather than abandoning the model.
If even high-confidence buckets fail, conclude model has no directional edge.
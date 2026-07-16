// FILE: feature_builder.go
// Package main – Unified AI feature construction.
//
// This file owns feature engineering only. It replaces the old
// BuildExtendedFeatures() split-path concept with ONE feature builder used by
// both training and live prediction.
//
// Design goals:
//   - One feature vector shape for train + inference
//   - No basic-vs-extended model distinction
//   - Soft judgment gates encoded as numeric model features
//   - Hard execution/risk gates remain outside the model, primarily in step.go
//   - Keep feature count disciplined to reduce rows-vs-columns overfitting risk
//   - Restore useful EMA structure that was lost in the MACD-only compression experiment
//   - Keep MACD line/histogram shape as compact momentum context
//   - Use only current MACD histogram delta plus light smoothing instead of d1/d2/d3 overload
//   - Add MACD regime flags so the model can learn strong positive/negative MACD zones
//
// Current hybrid feature vector, in order:
//
//   0  ret1
//      1-candle return
//
//   1  ret5
//      5-candle return
//
//   2  RSI14 / 100
//      normalized RSI
//
//   3  ZScore20
//      price deviation from rolling mean
//
//   4  ATR14 / Close
//      normalized volatility
//
//   5  realizedVol20 / Close
//      rolling standard deviation normalized by price
//
//   6  distance from recent high, pct
//
//   7  distance from recent low, pct
//
//   8  MACD line / Close
//      normalized MACD trend/momentum regime
//
//   9  MACD histogram / Close
//      normalized MACD momentum spread
//
//   10 MACD histogram delta d3 / Close
//      current normalized histogram change:
//      hist[idx] - hist[idx-1]
//
//   11 smoothed MACD histogram delta / Close
//      normalized short smoothing of recent histogram change:
//      (d2 + d3) / 2
//
//   12 MACD positive regime flag
//      1 when macdTurningPoint >= macdLineEPS, else 0
//
//   13 MACD negative regime flag
//      1 when macdTurningPoint <= -macdLineEPS, else 0
//
//   14 EMA4/EMA8 spread pct
//      (EMA4 - EMA8) / Close
//
//   15 EMA20/EMA50 spread pct
//      (EMA20 - EMA50) / Close
//
//   16 EMA20 slope
//      (EMA20[idx] - EMA20[idx-3]) / EMA20[idx-3]
//
//   17 EMA50 slope
//      (EMA50[idx] - EMA50[idx-3]) / EMA50[idx-3]
//
// Feature count: 18

package main

import "math"

// FeatureSnapshot contains the unified feature vector plus state values useful
// for audit logs, gates, and decision reason strings.
//
// Keep this struct broader than the model vector so gate/log code can inspect
// values without forcing every audit field to become a model feature.
type FeatureSnapshot struct {
	X []float64

	// Pattern booleans
	EMAHighPeak         bool
	EMALowBottom        bool
	EMAPriceDownGoingUp bool
	EMAPriceUpGoingDown bool

	// Short EMA state
	EMAFast          float64
	EMASlow          float64
	EMAFastPrev3     float64
	EMASlowPrev3     float64
	EMASpreadPct     float64
	EMAAlignStrength float64

	// MACD state
	MACDLine            float64
	MACDTurningPoint    float64
	MACDHist            float64
	MACDHistDelta       float64
	MACDHistDeltaSmooth float64
	MACDMomentumDown    bool
	MACDMomentumUp      bool
	MACDStrongPositive  bool
	MACDStrongNegative  bool

	// Medium/long EMA trend context
	EMA20           float64
	EMA50           float64
	EMA20Prev3      float64
	EMA50Prev3      float64
	EMA2050Spread   float64
	EMA2050Strength float64
	EMA20Slope      float64
	EMA50Slope      float64

	// Range context
	DistHighPct float64
	DistLowPct  float64
}

// BuildFeatureSnapshot builds the unified feature vector at candle index idx.
// It returns ok=false when there is not enough history or inputs are invalid.
//
// Feature vector vNext, in order:
//
//	0  ret1
//	1  ret5
//	2  RSI14 / 100
//	3  ZScore20
//	4  ATR14 / Close
//	5  realizedVol20 / Close
//	6  distance from recent high, pct
//	7  distance from recent low, pct
//	8  MACD line / Close
//	9  MACD histogram / Close
//	10 MACD histogram delta d3 / Close
//	11 smoothed MACD histogram delta / Close, (d2+d3)/2
//	12 EMA4/EMA8 spread pct
//	13 EMA4/EMA8 alignment strength, abs(spread)
//	14 EMA20/EMA50 spread pct
//	15 EMA20/EMA50 alignment strength, abs(spread)
//	16 EMA20 slope
//	17 EMA50 slope
//	18 EMA high-peak shape
//	19 EMA low-bottom shape
//	20 EMA price-down-going-up shape
//	21 EMA price-up-going-down shape
//	22 MACD high-peak shape
//	23 MACD low-bottom shape
//
// Feature count: 24
func BuildFeatureSnapshot(c []Candle, idx int, macdLineEPS float64, FeatureDim int) (FeatureSnapshot, bool) {
	var out FeatureSnapshot

	if len(c) < 60 || idx < 50 || idx >= len(c) || idx-6 < 0 {
		return out, false
	}
	if c[idx].Close <= 0 || c[idx-1].Close <= 0 || c[idx-5].Close <= 0 {
		return out, false
	}

	closePx := make([]float64, len(c))
	for i := range c {
		closePx[i] = c[i].Close
	}

	rsis := RSI(c, 14)
	zs := ZScore(c, 20)
	atr := ATR(c, 14)
	std20 := RollingStd(closePx, 20)

	ema4 := EMA(closePx, 4)
	ema8 := EMA(closePx, 8)
	ema20 := EMA(closePx, 20)
	ema50 := EMA(closePx, 50)

	macdLine, macdHist, _, d2, d3, ok := MACDLineHistAndSlopesAt(c, idx)
	if !ok {
		return out, false
	}

	fast := ema4[idx]
	slow := ema8[idx]
	fast2 := ema4[idx-2]
	slow2 := ema8[idx-2]
	fast4 := ema4[idx-4]
	slow4 := ema8[idx-4]
	fast6 := ema4[idx-6]
	slow6 := ema8[idx-6]

	mid := ema20[idx]
	long := ema50[idx]
	midPrev3 := ema20[idx-3]
	longPrev3 := ema50[idx-3]

	macdLineNow := macdLine[idx]
	macdTurningPoint := macdLine[idx-2]
	histNow := macdHist[idx]
	histDeltaNow := d3
	histDeltaSmooth := (d2 + d3) / 2.0

	emaHighPeak := false
	emaLowBottom := false
	emaPriceDownGoingUp := false
	emaPriceUpGoingDown := false

	if !badFloat(fast) && !badFloat(slow) &&
		!badFloat(fast2) && !badFloat(slow2) &&
		!badFloat(fast4) && !badFloat(slow4) &&
		!badFloat(fast6) && !badFloat(slow6) {

		fastIsHigher := (fast > slow) && (fast4 > slow4) && (fast6 > slow6)
		fastIsLower := (fast < slow) && (fast4 < slow4) && (fast6 < slow6)

		// EMA reversal / exhaustion geometry restored from the stronger old feature era.
		emaPriceDownGoingUp = (fast < slow) &&
			(fast6 < slow6) &&
			(slow-fast < slow6-fast6)

		emaPriceUpGoingDown = (fast > slow) &&
			(fast6 > slow6) &&
			(fast-slow < fast6-slow6)

		emaHighPeak = fastIsHigher &&
			(fast4-slow4 > fast6-slow6) &&
			(fast2-slow2 > fast4-slow4) &&
			(fast-slow < fast2-slow2)

		emaLowBottom = fastIsLower &&
			(slow4-fast4 > slow6-fast6) &&
			(slow2-fast2 > slow4-fast4) &&
			(slow-fast < slow2-fast2)
	}

	// ---------------------------------------------------------------------
	// MACD raw pattern materials
	// Keep atomic facts separate so the model learns combinations itself
	// instead of us pre-combining assumptions.
	// ---------------------------------------------------------------------

	// Momentum direction
	macdMomentumDown := histDeltaNow < 0 && histNow > 0
	macdMomentumUp := histDeltaNow > 0 && histNow < 0

	// Strength / turning evidence
	macdStrongPositive := macdTurningPoint >= macdLineEPS
	macdStrongNegative := macdTurningPoint <= -macdLineEPS

	ret1 := safeRatio(c[idx].Close-c[idx-1].Close, c[idx-1].Close)
	ret5 := safeRatio(c[idx].Close-c[idx-5].Close, c[idx-5].Close)
	atrPct := safeRatio(atr[idx], c[idx].Close)
	volPct := safeRatio(std20[idx], c[idx].Close)

	emaSpreadPct := safeRatio(fast-slow, c[idx].Close)
	emaAlignStrength := math.Abs(emaSpreadPct)
	ema2050Spread := safeRatio(mid-long, c[idx].Close)
	ema2050Strength := math.Abs(ema2050Spread)
	ema20Slope := safeRatio(mid-midPrev3, midPrev3)
	ema50Slope := safeRatio(long-longPrev3, longPrev3)

	recentHigh, recentLow := recentHighLow(c, idx, 20)
	distHighPct := 0.0
	if recentHigh > 0 {
		distHighPct = safeRatio(recentHigh-c[idx].Close, recentHigh)
	}
	distLowPct := 0.0
	if recentLow > 0 {
		distLowPct = safeRatio(c[idx].Close-recentLow, recentLow)
	}

	macdScale := c[idx].Close

	x := []float64{
		ret1,
		ret5,
		safeRatio(rsis[idx], 100.0),
		zs[idx],
		atrPct,
		volPct,
		distHighPct,
		distLowPct,

		safeRatio(macdLineNow, macdScale),
		safeRatio(histNow, macdScale),
		safeRatio(histDeltaNow, macdScale),
		safeRatio(histDeltaSmooth, macdScale),

		emaSpreadPct,
		emaAlignStrength,
		ema2050Spread,
		ema2050Strength,
		ema20Slope,
		ema50Slope,

		boolToFloat(emaHighPeak),
		boolToFloat(emaLowBottom),
		boolToFloat(emaPriceDownGoingUp),
		boolToFloat(emaPriceUpGoingDown),
		boolToFloat(macdStrongPositive),
		boolToFloat(macdStrongNegative),
	}

	if len(x) != FeatureDim || hasBadFloat(x) {
		return out, false
	}

	out = FeatureSnapshot{
		X:                   x,
		EMAHighPeak:         emaHighPeak,
		EMALowBottom:        emaLowBottom,
		EMAPriceDownGoingUp: emaPriceDownGoingUp,
		EMAPriceUpGoingDown: emaPriceUpGoingDown,
		EMAFast:             fast,
		EMASlow:             slow,
		EMAFastPrev3:        fast4,
		EMASlowPrev3:        slow4,
		EMASpreadPct:        emaSpreadPct,
		EMAAlignStrength:    emaAlignStrength,
		MACDLine:            macdLineNow,
		MACDTurningPoint:    macdTurningPoint,
		MACDHist:            histNow,
		MACDHistDelta:       histDeltaNow,
		MACDHistDeltaSmooth: histDeltaSmooth,
		MACDMomentumDown:    macdMomentumDown,
		MACDMomentumUp:      macdMomentumUp,
		MACDStrongPositive:  macdStrongPositive,
		MACDStrongNegative:  macdStrongNegative,
		EMA20:               mid,
		EMA50:               long,
		EMA20Prev3:          midPrev3,
		EMA50Prev3:          longPrev3,
		EMA2050Spread:       ema2050Spread,
		EMA2050Strength:     ema2050Strength,
		EMA20Slope:          ema20Slope,
		EMA50Slope:          ema50Slope,
		DistHighPct:         distHighPct,
		DistLowPct:          distLowPct,
	}
	return out, true
}

// BuildFeatures returns only the numeric vector for model training/prediction.
func BuildFeatures(c []Candle, idx int, macdLineEPS float64, FeatureDim int) ([]float64, bool) {
	snap, ok := BuildFeatureSnapshot(c, idx, macdLineEPS, FeatureDim)
	if !ok {
		return nil, false
	}
	return snap.X, true
}

func boolToFloat(v bool) float64 {
	if v {
		return 1.0
	}
	return 0.0
}

func safeRatio(num, den float64) float64 {
	if den == 0 || badFloat(num) || badFloat(den) {
		return 0
	}
	v := num / den
	if badFloat(v) {
		return 0
	}
	return v
}

func badFloat(v float64) bool {
	return math.IsNaN(v) || math.IsInf(v, 0)
}

func hasBadFloat(xs []float64) bool {
	for _, v := range xs {
		if badFloat(v) {
			return true
		}
	}
	return false
}

func recentHighLow(c []Candle, idx int, lookback int) (float64, float64) {
	if idx < 0 || idx >= len(c) || lookback <= 0 {
		return 0, 0
	}
	start := idx - lookback + 1
	if start < 0 {
		start = 0
	}
	hi := c[start].High
	lo := c[start].Low
	for i := start + 1; i <= idx; i++ {
		if c[i].High > hi {
			hi = c[i].High
		}
		if c[i].Low < lo {
			lo = c[i].Low
		}
	}
	return hi, lo
}

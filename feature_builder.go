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
//
// Current feature vector, in order:
//   0  ret1
//   1  ret5
//   2  RSI14 / 100
//   3  ZScore20
//   4  ATR14 / Close
//   5  realizedVol20 / Close
//   6  EMA spread pct: (EMA4 - EMA8) / Close
//   7  EMA alignment strength: abs(EMA4 - EMA8) / Close
//   8  HighPeak bool
//   9  LowBottom bool
//   10 PriceDownGoingUp bool
//   11 PriceUpGoingDown bool
//   12 distance from recent high, pct
//   13 distance from recent low, pct
//   14 MACD histogram
//   15 MACD histogram delta
//
// Feature count: 20

package main

import "math"

const UnifiedFeatureDim = 20

// FeatureSnapshot contains the unified feature vector plus
// pattern-state values useful for audit logs and decisions.
type FeatureSnapshot struct {
	X []float64

	// Pattern booleans
	HighPeak  bool
	LowBottom bool

	// MACD state
	MACDLine float64
	MACDHist float64
	MACDD1   float64
	MACDD2   float64
	MACDD3   float64

	// Optional context for logs/debug
	DistHighPct float64
	DistLowPct  float64
}

// BuildFeatureSnapshot builds the unified feature vector at candle index idx.
// It returns ok=false when there is not enough history or inputs are invalid.
func BuildFeatureSnapshot(c []Candle, idx int) (FeatureSnapshot, bool) {
	var out FeatureSnapshot

	if len(c) < 60 || idx < 50 || idx >= len(c) || idx-6 < 0 {
		return out, false
	}
	if c[idx].Close <= 0 || c[idx-1].Close <= 0 || c[idx-5].Close <= 0 {
		return out, false
	}

	close := make([]float64, len(c))
	for i := range c {
		close[i] = c[i].Close
	}

	rsis := RSI(c, 14)
	zs := ZScore(c, 20)
	atr := ATR(c, 14)
	std20 := RollingStd(close, 20)
	ema4 := EMA(close, 4)
	ema8 := EMA(close, 8)
	macdLine, macdHist, d1, d2, d3, ok := MACDLineHistAndSlopes(c)
	if !ok {
		return out, false
	}
	ema20 := EMA(close, 20)
	ema50 := EMA(close, 50)

	fast := ema4[idx]
	slow := ema8[idx]
	fast2 := ema4[idx-2]
	slow2 := ema8[idx-2]
	fast4 := ema4[idx-4]
	slow4 := ema8[idx-4]
	fast6 := ema4[idx-6]
	slow6 := ema8[idx-6]
	macdLineNow := macdLine[idx]
	histNow := macdHist[idx]
	mid := ema20[idx]
	long := ema50[idx]
	midPrev3 := ema20[idx-3]
	longPrev3 := ema50[idx-3]

	highPeak := false
	lowBottom := false
	// priceDownGoingUp := false
	// priceUpGoingDown := false

	if !badFloat(fast) && !badFloat(slow) && !badFloat(fast2) && !badFloat(slow2) && !badFloat(fast4) && !badFloat(slow4) && !badFloat(fast6) && !badFloat(slow6) {
		// fastIsHigher := (fast > slow) && (fast4 > slow4) && (fast6 > slow6)
		// fastIsLower := (fast < slow) && (fast4 < slow4) && (fast6 < slow6)
		// priceDownGoingUp = (fast < slow) && (fast6 < slow6) && (slow-fast < slow6-fast6)
		// priceUpGoingDown = (fast > slow) && (fast6 > slow6) && (fast-slow < fast6-slow6)
		// highPeak = fastIsHigher && (fast4-slow4 > fast6-slow6) && (fast2-slow2 > fast4-slow4) && (fast-slow < fast2-slow2)
		// lowBottom = fastIsLower && (slow4-fast4 > slow6-fast6) && (slow2-fast2 > slow4-fast4) && (slow-fast < slow2-fast2)
		highPeak = d1 > 0 && d2 > 0 && d3 < 0 && histNow > 0
		lowBottom = d1 < 0 && d2 < 0 && d3 > 0 && histNow < 0
	}

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

	x := []float64{
		ret1,
		ret5,
		safeRatio(rsis[idx], 100.0),
		zs[idx],
		atrPct,
		volPct,
		emaSpreadPct,
		emaAlignStrength,
		boolToFloat(highPeak),
		boolToFloat(lowBottom),
		// boolToFloat(priceDownGoingUp),
		// boolToFloat(priceUpGoingDown),
		distHighPct,
		distLowPct,
		histNow,
		// histDelta,
		ema2050Spread,
		ema2050Strength,
		ema20Slope,
		ema50Slope,
	}

	if len(x) != UnifiedFeatureDim || hasBadFloat(x) {
		return out, false
	}

	out = FeatureSnapshot{
		X:           x,
		HighPeak:    highPeak,
		LowBottom:   lowBottom,
		MACDHist:    histNow,
		MACDLine:    macdLineNow,
		MACDD1:      d1,
		MACDD2:      d2,
		MACDD3:      d3,
		DistHighPct: distHighPct,
		DistLowPct:  distLowPct,
	}
	return out, true
}

// BuildFeatures returns only the numeric vector for model training/prediction.
func BuildFeatures(c []Candle, idx int) ([]float64, bool) {
	snap, ok := BuildFeatureSnapshot(c, idx)
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

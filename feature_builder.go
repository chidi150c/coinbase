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
//   - Reduce multicollinearity from overlapping EMA-derived features
//   - Use MACD as a compressed momentum/trend representation
//
// Current feature vector, in order:
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
//   6  HighPeak bool
//      MACD histogram top rollover pattern:
//      rising → rising → falling above zero
//
//   7  LowBottom bool
//      MACD histogram bottom reversal pattern:
//      falling → falling → rising below zero
//
//   8  distance from recent high, pct
//
//   9  distance from recent low, pct
//
//   10 MACD line
//      primary trend/momentum regime
//
//   11 MACD histogram slope d1
//      hist[idx-2] - hist[idx-3]
//
//   12 MACD histogram slope d2
//      hist[idx-1] - hist[idx-2]
//
//   13 MACD histogram slope d3
//      hist[idx] - hist[idx-1]
//
// Feature count: 14

package main

import "math"

const UnifiedFeatureDim = 14

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

	if len(c) < 60 || idx < 50 || idx >= len(c) || idx-5 < 0 {
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

	macdLine, macdHist, d1, d2, d3, ok := MACDLineHistAndSlopesAt(c, idx)
	if !ok {
		return out, false
	}

	macdLineNow := macdLine[idx]
	histNow := macdHist[idx]

	highPeak := d1 > 0 && d2 > 0 && d3 < 0 && histNow > 0
	lowBottom := d1 < 0 && d2 < 0 && d3 > 0 && histNow < 0

	ret1 := safeRatio(c[idx].Close-c[idx-1].Close, c[idx-1].Close)
	ret5 := safeRatio(c[idx].Close-c[idx-5].Close, c[idx-5].Close)
	atrPct := safeRatio(atr[idx], c[idx].Close)
	volPct := safeRatio(std20[idx], c[idx].Close)

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
		boolToFloat(highPeak),
		boolToFloat(lowBottom),
		distHighPct,
		distLowPct,
		macdLineNow,
		d1,
		d2,
		d3,
	}

	if len(x) != UnifiedFeatureDim || hasBadFloat(x) {
		return out, false
	}

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

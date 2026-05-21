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
// Feature count: 16

package main

import "math"

const UnifiedFeatureDim = 16

// FeatureSnapshot contains the unified feature vector plus the soft-gate booleans
// that are useful for decision audit strings and logs.
type FeatureSnapshot struct {
	X []float64

	HighPeak         bool
	LowBottom        bool
	PriceDownGoingUp bool
	PriceUpGoingDown bool

	EMAFast          float64
	EMASlow          float64
	EMAFastPrev3     float64
	EMASlowPrev3     float64
	EMASpreadPct     float64
	EMAAlignStrength float64
	MACDHist      float64
	MACDHistDelta float64
}

// BuildFeatureSnapshot builds the unified feature vector at candle index idx.
// It returns ok=false when there is not enough history or inputs are invalid.
func BuildFeatureSnapshot(c []Candle, idx int) (FeatureSnapshot, bool) {
	var out FeatureSnapshot

	if len(c) < 60 || idx < 26 || idx >= len(c) || idx-5 < 0 || idx-3 < 0 {
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
	_, _, macdHist := MACD(close, 12, 26, 9)

	fast := ema4[idx]
	slow := ema8[idx]
	fast2rd := ema4[idx-2]
	slow2rd := ema8[idx-2]
	fast3rd := ema4[idx-3]
	slow3rd := ema8[idx-3]
	histNow := macdHist[idx]
	histPrev := macdHist[idx-1]
	histDelta := histNow - histPrev

	highPeak := false
	lowBottom := false
	priceDownGoingUp := false
	priceUpGoingDown := false

	if !badFloat(fast) && !badFloat(slow) && !badFloat(fast2rd) && !badFloat(slow2rd) && !badFloat(fast3rd) && !badFloat(slow3rd) {
		highPeak = (slow3rd < fast3rd) && (slow2rd-fast2rd > slow3rd-fast3rd) && (slow-fast < slow2rd-fast2rd) && (slow < fast)
		priceDownGoingUp = (slow > fast) && (slow-fast < slow3rd-fast3rd) && (slow3rd > fast3rd)
		lowBottom = (fast3rd < slow3rd) && (fast2rd-slow2rd > fast3rd-slow3rd) && (fast-slow < fast2rd-slow2rd) && (fast < slow)
		priceUpGoingDown = (fast > slow) && (fast-slow < fast3rd-slow3rd) && (fast3rd > slow3rd)
	}

	ret1 := safeRatio(c[idx].Close-c[idx-1].Close, c[idx-1].Close)
	ret5 := safeRatio(c[idx].Close-c[idx-5].Close, c[idx-5].Close)
	atrPct := safeRatio(atr[idx], c[idx].Close)
	volPct := safeRatio(std20[idx], c[idx].Close)
	emaSpreadPct := safeRatio(fast-slow, c[idx].Close)
	emaAlignStrength := math.Abs(emaSpreadPct)

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
		boolToFloat(priceDownGoingUp),
		boolToFloat(priceUpGoingDown),
		distHighPct,
		distLowPct,
		histNow,
		histDelta,
	}

	if len(x) != UnifiedFeatureDim || hasBadFloat(x) {
		return out, false
	}

	out = FeatureSnapshot{
		X:                x,
		HighPeak:         highPeak,
		LowBottom:        lowBottom,
		PriceDownGoingUp: priceDownGoingUp,
		PriceUpGoingDown: priceUpGoingDown,
		EMAFast:          fast,
		EMASlow:          slow,
		EMAFastPrev3:     fast3rd,
		EMASlowPrev3:     slow3rd,
		EMASpreadPct:     emaSpreadPct,
		EMAAlignStrength: emaAlignStrength,
		MACDHist:         histNow,
		MACDHistDelta:    histDelta,
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

// FILE: strategy.go
// Package main – Core trading abstractions and decision logic.
//
// This file declares the market data types used across the bot (Candle),
// the signal enums (Buy/Sell/Flat), metadata about a decision, and the
// `decide` function that turns recent candles into a trading intent.
//
// The decision blends:
//   • A tiny ML “micro-model” probability pUp (see model.go)
//   • A moving-average regime filter (MA10 vs MA30), optionally enabled
//     via USE_MA_FILTER (see env.go thresholds).
//
// Thresholds are tunable via .env (no exports):
//   BUY_THRESHOLD (default 0.55), SELL_THRESHOLD (default 0.45),
//   USE_MA_FILTER (default true)

package main

import (
	"fmt"
	"log"
	"math"
	"strings"
	"time"
)

// Candle is the normalized OHLCV row the bot uses everywhere.
type Candle struct {
	Time   time.Time
	Open   float64
	High   float64
	Low    float64
	Close  float64
	Volume float64
}

// Signal is the high-level intent.
type Signal int

// String implements fmt.Stringer for pretty logging.
func (s Signal) String() string {
	switch s {
	case Buy:
		return "BUY"
	case Sell:
		return "SELL"
	default:
		return "FLAT"
	}
}

const (
	Flat Signal = iota
	Buy
	Sell
)

// Decision captures what to do and why.
type Decision struct {
	Signal     Signal
	Confidence float64
	Reason     string

	// NEW: carry raw pUp and MA regime flags for downstream gate-audit strings
	PUp              float64
	HighPeak         bool
	PriceDownGoingUp bool
	LowBottom        bool
	PriceUpGoingDown bool
}

// SignalToSide converts the intent into a broker side.
func (d Decision) SignalToSide() OrderSide {
	switch d.Signal {
	case Buy:
		return SideBuy
	case Sell:
		return SideSell
	default:
		return SideBuy
	}
}

// decide computes a trading decision from recent candles and the model.
func decide(c []Candle, m *AIMicroModel, mdl *ExtendedLogit, buyThreshold float64, sellThreshold float64, useMAFilter bool) Decision {
	if len(c) < 40 {
		return Decision{Signal: Flat, Confidence: 0, Reason: "not_enough_data", PUp: 0.0}
	}
	i := len(c) - 1

	// --- NEW: debug last/prev close before computing features
	if i-1 >= 0 {
		//log.Printf("[DEBUG] lastClose=%.2f prevClose=%.2f", c[i].Close, c[i-1].Close)
	}
	// ---

	// Features
	rsis := RSI(c, 14)
	zs := ZScore(c, 20)
	ret1 := (c[i].Close - c[i-1].Close) / c[i-1].Close
	ret5 := (c[i].Close - c[i-5].Close) / c[i-5].Close
	features := []float64{ret1, ret5, rsis[i] / 100.0, zs[i]}

	// Base pUp from the micro-model
	pUp := m.predict(features)

	// If MODEL_MODE=extended and an extended model exists, use it for pUp.
	if strings.EqualFold(getEnv("MODEL_MODE", "baseline"), "extended") {
		if mdl != nil {
			// ComputePUpextended logs a debug line when features/model exist.
			pUp = ComputePUpextended(c, mdl)
		} else {
			log.Printf("[DEBUG] extended mode requested but no mdlExt present; using micro-model pUp")
		}
	}

	// Regime filter (Coinbase EMA): fast = EMA(close,4), slow = EMA(close,8)
	cl := make([]float64, len(c))
	for k := range c {
		cl[k] = c[k].Close
	}
	ema4 := EMA(cl, 4)
	ema8 := EMA(cl, 8)
	fast := ema4[i]
	slow := ema8[i]
	fast2rd := ema4[i-2]
	slow2rd := ema8[i-2]
	fast3rd := ema4[i-3]
	slow3rd := ema8[i-3]
	buyMASignal := false
	sellMASignal := false

	HighPeak := false
	PriceDownGoingUp := false
	LowBottom := false
	PriceUpGoingDown := false

	if !math.IsNaN(fast) && !math.IsNaN(slow) && !math.IsNaN(fast3rd) && !math.IsNaN(slow3rd) {
		HighPeak = (slow3rd < fast3rd) && (slow2rd-fast2rd > slow3rd-fast3rd) && (slow-fast < slow2rd-fast2rd) && (slow < fast)
		PriceDownGoingUp = (slow > fast) && (slow-fast < slow3rd-fast3rd) && (slow3rd > fast3rd)
		LowBottom = (fast3rd < slow3rd) && (fast2rd-slow2rd > fast3rd-slow3rd) && (fast-slow < fast2rd-slow2rd) && (fast < slow)
		PriceUpGoingDown = (fast > slow) && (fast-slow < fast3rd-slow3rd) && (fast3rd > slow3rd)

		if LowBottom {
			buyMASignal = true
			log.Printf("[DEBUG] MA Signalled LowBottom: BUY: HighPeak: %v, PriceDownGoingUp: %v, LowBottom: %v, PriceUpGoingDown: %v", HighPeak, PriceDownGoingUp, LowBottom, PriceUpGoingDown)
		} else if HighPeak {
			sellMASignal = true
			log.Printf("[DEBUG] MA Signalled HighPeak: SELL: HighPeak: %v, PriceDownGoingUp: %v, LowBottom: %v, PriceUpGoingDown: %v", HighPeak, PriceDownGoingUp, LowBottom, PriceUpGoingDown)
		} else if PriceDownGoingUp {
			buyMASignal = true
			log.Printf("[DEBUG] MA Signalled PriceDownGoingUp: BUY: HighPeak: %v, PriceDownGoingUp: %v, LowBottom: %v, PriceUpGoingDown: %v", HighPeak, PriceDownGoingUp, LowBottom, PriceUpGoingDown)
		} else if PriceUpGoingDown {
			sellMASignal = true
			log.Printf("[DEBUG] MA Signalled PriceUpGoingDown: SELL: HighPeak: %v, PriceDownGoingUp: %v, LowBottom: %v, PriceUpGoingDown: %v", HighPeak, PriceDownGoingUp, LowBottom, PriceUpGoingDown)
		}
	}

	reason := fmt.Sprintf("pUp=%.5f, ema4=%.2f vs ema8=%.2f, ema4_3rd=%.2f vs ema8_3rd=%.2f", pUp, fast, slow, fast3rd, slow3rd)

	// BUY if pUp clears threshold and (optionally) MA filter
	if pUp > buyThreshold && (!useMAFilter || buyMASignal) {
		return Decision{
			Signal:     Buy,
			Confidence: pUp,
			Reason:     reason,
			PUp:        pUp,
			HighPeak:   HighPeak, PriceDownGoingUp: PriceDownGoingUp, LowBottom: LowBottom, PriceUpGoingDown: PriceUpGoingDown,
		}
	}
	// SELL if pUp below threshold and (optionally) MA filter is bearish
	if pUp < sellThreshold && (!useMAFilter || sellMASignal) {
		return Decision{
			Signal:     Sell,
			Confidence: 1 - pUp,
			Reason:     reason,
			PUp:        pUp,
			HighPeak:   HighPeak, PriceDownGoingUp: PriceDownGoingUp, LowBottom: LowBottom, PriceUpGoingDown: PriceUpGoingDown,
		}
	}
	return Decision{
		Signal:     Flat,
		Confidence: 0.5,
		Reason:     reason,
		PUp:        pUp,
		HighPeak:   HighPeak, PriceDownGoingUp: PriceDownGoingUp, LowBottom: LowBottom, PriceUpGoingDown: PriceUpGoingDown,
	}
}

// ---- Extended features + pUp helper (opt-in; baseline remains unchanged) ----

// BuildExtendedFeatures constructs a richer feature set for the extended model.
// If train==true, it also returns next-bar "up" labels; otherwise labels is nil.
// Features per row:
//   [ ret1, ret5, RSI14/100, ZScore20, ATR14/Close, MACD_hist(12,26,9), OBV_norm, Std20/Close ]
func BuildExtendedFeatures(c []Candle, train bool) ([][]float64, []float64) {
	if len(c) < 60 {
		return nil, nil
	}
	close := make([]float64, len(c))
	for i := range c {
		close[i] = c[i].Close
	}

	rsis := RSI(c, 14)
	zs := ZScore(c, 20)
	atr := ATR(c, 14)
	atrPct := make([]float64, len(c))
	for i := range c {
		if c[i].Close > 0 {
			atrPct[i] = atr[i] / c[i].Close
		}
	}

	_, _, hist := MACD(close, 12, 26, 9)
	obv := OBV(c)
	// Normalize OBV by max-abs to keep it bounded
	maxAbs := 1.0
	for i := range obv {
		if v := math.Abs(obv[i]); v > maxAbs {
			maxAbs = v
		}
	}
	obvN := make([]float64, len(obv))
	for i := range obv {
		obvN[i] = obv[i] / maxAbs
	}

	std20 := RollingStd(close, 20)

	feats := [][]float64{}
	var labels []float64

	// --- change: include the last (nudged) candle for inference ---
	start := 26
	end := len(c) - 1
	if !train {
		end = len(c) // inference path includes c[len(c)-1]
	}
	for i := start; i < end; i++ {
		ret1 := (c[i].Close - c[i-1].Close) / (c[i-1].Close + 1e-12)
		ret5 := (c[i].Close - c[i-5].Close) / (c[i-5].Close + 1e-12)
		volPct := std20[i] / (c[i].Close + 1e-12)
		f := []float64{
			ret1,
			ret5,
			rsis[i] / 100.0,
			zs[i],
			atrPct[i],
			hist[i],
			obvN[i],
			volPct,
		}
		feats = append(feats, f)
		if train {
			up := 0.0
			if c[i+1].Close > c[i].Close {
				up = 1.0
			}
			labels = append(labels, up)
		}
	}
	return feats, labels
}

// ComputePUpextended returns pUp from the extended logistic model for the
// most recent feature row. If features are unavailable, returns 0.5.
func ComputePUpextended(c []Candle, mdl *ExtendedLogit) float64 {
	fe, _ := BuildExtendedFeatures(c, false)

	// Only log if we actually have features
	if len(fe) == 0 || mdl == nil {
		log.Printf("[DEBUG] pUp: no features or model (len(fe)=%d, mdl_nil=%v)", len(fe), mdl == nil)
		return 0.5
	}

	last := fe[len(fe)-1]
	// log.Printf("[DEBUG] features[n-1]=%+v", last)
	return mdl.Predict(last)
}

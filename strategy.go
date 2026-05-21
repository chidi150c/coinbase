// FILE: strategy.go
// Package main – Core trading abstractions and decision logic.
//
// This file declares the market data types used across the bot (Candle),
// the signal enums (Buy/Sell/Flat), metadata about a decision, and the
// `decide` function that turns recent candles into a trading intent.
//
// Decision responsibility is intentionally narrow:
//   • Ask feature_builder.go for the unified market/soft-gate snapshot
//   • Ask the unified logistic model for pUp from that feature vector
//   • Convert pUp into BUY / SELL / FLAT using configured thresholds
//   • Optionally preserve the existing MA soft-gate filter during transition
//
// Execution, funding, exchange validity, pending orders, lot caps, and risk
// controls remain deterministic hard gates in step.go.

package main

import (
	"fmt"
	"log"
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

	// Carry raw pUp and soft-gate flags for downstream gate-audit strings.
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

// decide computes a trading decision from recent candles and the unified model.
func decide(c []Candle, mdl *LogisticModel, buyThreshold float64, sellThreshold float64, useMAFilter bool) Decision {
	if len(c) < 60 {
		return Decision{Signal: Flat, Confidence: 0, Reason: "not_enough_data", PUp: 0.0}
	}

	idx := len(c) - 1
	snap, ok := BuildFeatureSnapshot(c, idx)
	if !ok {
		return Decision{Signal: Flat, Confidence: 0, Reason: "not_enough_features", PUp: 0.0}
	}

	// Single prediction path: the same unified feature vector is used for
	// inference here and for training in dataset.go.
	pUp := 0.5
	if mdl != nil {
		pUp = mdl.Predict(snap.X)
	}

	buyMASignal := snap.LowBottom || snap.PriceDownGoingUp
	sellMASignal := snap.HighPeak || snap.PriceUpGoingDown

	if snap.LowBottom {
		logSoftGate("LowBottom", "BUY", snap)
	} else if snap.HighPeak {
		logSoftGate("HighPeak", "SELL", snap)
	} else if snap.PriceDownGoingUp {
		logSoftGate("PriceDownGoingUp", "BUY", snap)
	} else if snap.PriceUpGoingDown {
		logSoftGate("PriceUpGoingDown", "SELL", snap)
	}

	reason := fmt.Sprintf(
		"pUp=%.5f, ema4=%.2f vs ema8=%.2f, ema4_3rd=%.2f vs ema8_3rd=%.2f, emaSpreadPct=%.6f, emaAlign=%.6f",
		pUp,
		snap.EMAFast,
		snap.EMASlow,
		snap.EMAFastPrev3,
		snap.EMASlowPrev3,
		snap.EMASpreadPct,
		snap.EMAAlignStrength,
	)

	base := Decision{
		Confidence:       0.5,
		Reason:           reason,
		PUp:              pUp,
		HighPeak:         snap.HighPeak,
		PriceDownGoingUp: snap.PriceDownGoingUp,
		LowBottom:        snap.LowBottom,
		PriceUpGoingDown: snap.PriceUpGoingDown,
	}

	// BUY if pUp clears threshold and the transitional MA filter allows it.
	if pUp > buyThreshold && (!useMAFilter || buyMASignal) {
		base.Signal = Buy
		base.Confidence = pUp
		return base
	}

	// SELL if pUp falls below threshold and the transitional MA filter allows it.
	if pUp < sellThreshold && (!useMAFilter || sellMASignal) {
		base.Signal = Sell
		base.Confidence = 1 - pUp
		return base
	}

	base.Signal = Flat
	return base
}

func logSoftGate(name string, side string, snap FeatureSnapshot) {
	log.Printf(
		"[DEBUG] MA Signalled %s: %s: HighPeak: %v, PriceDownGoingUp: %v, LowBottom: %v, PriceUpGoingDown: %v",
		name,
		side,
		snap.HighPeak,
		snap.PriceDownGoingUp,
		snap.LowBottom,
		snap.PriceUpGoingDown,
	)
}

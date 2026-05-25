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
func decide(c []Candle, mdl *LogisticModel, buyThreshold float64, sellThreshold float64) Decision {
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

	reason := fmt.Sprintf(
		"pUp=%.5f, ema4=%.2f vs ema8=%.2f, ema4_3rd=%.2f vs ema8_3rd=%.2f, emaSpreadPct=%.6f, emaAlign=%.6f, macdHist=%.5f, macdDelta=%.5f, ema2050Spread=%.6f, ema20Slope=%.6f, ema50Slope=%.6f",
		pUp,
		snap.EMAFast,
		snap.EMASlow,
		snap.EMAFastPrev3,
		snap.EMASlowPrev3,
		snap.EMASpreadPct,
		snap.EMAAlignStrength,
		snap.MACDHist,
		snap.MACDHistDelta,
		snap.EMA2050Spread,
		snap.EMA20Slope,
		snap.EMA50Slope,
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

	// BUY if pUp clears threshold.
	if pUp > buyThreshold{
		base.Signal = Buy
		base.Confidence = pUp
		return base
	}

	// SELL if pUp falls below threshold.
	if pUp < sellThreshold {
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

func (t *Trader) applyMACDSlopeGate(d Decision,	execHistory []Candle) Decision {

	if !getEnvBool("USE_MACD_SLOPE_GATE", false) {
		return d
	}

	if d.Signal != Buy && d.Signal != Sell {
		return d
	}

	slope, ok := macdHistSlope(execHistory)
	if !ok {
		log.Printf(
			"[MACD_GATE] skip insufficient_history len=%d",
			len(execHistory),
		)
		return d
	}

	eps := getEnvFloat("MACD_SLOPE_EPS", 0.0)

	raw := d.Signal
	reason := ""

	switch d.Signal {

	case Sell:
		if slope > eps {
			d.Signal = Flat
			reason = d.Reason + " | " + "bullish_macd_against_sell"
		}

	case Buy:
		if slope < -eps {
			d.Signal = Flat
			reason = d.Reason + " | " + "bearish_macd_against_buy"
		}
	}

	log.Printf(
		"[MACD_GATE] raw=%s final=%s slope=%.8f eps=%.8f reason=%s",
		raw,
		d.Signal,
		slope,
		eps,
		reason,
	)

	return d
}

func (t *Trader) applyMAFilterGate(d Decision, execHistory []Candle) Decision {
	if !t.cfg.UseMAFilter {
		return d
	}

	if d.Signal != Buy && d.Signal != Sell {
		return d
	}

	if len(execHistory) < 60 {
		return d
	}

	snap, ok := BuildFeatureSnapshot(execHistory, len(execHistory)-1)
	if !ok {
		return d
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

	raw := d.Signal

	if d.Signal == Buy && !buyMASignal {
		d.Signal = Flat
		d.Reason += " | ma_gate_block_buy"
	}

	if d.Signal == Sell && !sellMASignal {
		d.Signal = Flat
		d.Reason += " | ma_gate_block_sell"
	}

	log.Printf(
		"[MA_GATE] raw=%s final=%s buyMA=%v sellMA=%v lowBottom=%v priceDownGoingUp=%v highPeak=%v priceUpGoingDown=%v",
		raw,
		d.Signal,
		buyMASignal,
		sellMASignal,
		snap.LowBottom,
		snap.PriceDownGoingUp,
		snap.HighPeak,
		snap.PriceUpGoingDown,
	)

	return d
}

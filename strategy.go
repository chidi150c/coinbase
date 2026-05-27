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
	Raw 	   Signal
	Confidence float64
	Reason     string

	// Carry raw pUp and soft-gate flags for downstream gate-audit strings.
	PUp              float64
	HighPeak         bool
	PriceDownGoingUp bool
	LowBottom        bool
	PriceUpGoingDown bool
	MACDHist         float64
	MACDHistDelta    float64
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

// decide computes a trading decision from signal timeframe candles and the unified model.
func (t *Trader) decide(signalHistory []Candle) Decision {
	if len(signalHistory) < 60 {
		return Decision{
			Signal:     Flat,
			Confidence: 0,
			Reason:     "not_enough_data",
			PUp:        0.0,
		}
	}

	idx := len(signalHistory) - 1
	snap, ok := BuildFeatureSnapshot(signalHistory, idx)
	if !ok {
		return Decision{
			Signal:     Flat,
			Confidence: 0,
			Reason:     "not_enough_features",
			PUp:        0.0,
		}
	}

	pUp := 0.5
	if t.model != nil {
		pUp = t.model.Predict(snap.X)
	}

	signalTF := t.cfg.SignalTF()

	reason := fmt.Sprintf(
		"pUp=%.5f, ema4_%s=%.2f vs ema8_%s=%.2f, ema4_3rd_%s=%.2f vs ema8_3rd_%s=%.2f, emaSpreadPct_%s=%.6f, emaAlign_%s=%.6f, macdHist_%s=%.5f, macdDelta_%s=%.5f, ema2050Spread_%s=%.6f, ema20Slope_%s=%.6f, ema50Slope_%s=%.6f",
		pUp,
		signalTF, snap.EMAFast,
		signalTF, snap.EMASlow,
		signalTF, snap.EMAFastPrev3,
		signalTF, snap.EMASlowPrev3,
		signalTF, snap.EMASpreadPct,
		signalTF, snap.EMAAlignStrength,
		signalTF, snap.MACDHist,
		signalTF, snap.MACDHistDelta,
		signalTF, snap.EMA2050Spread,
		signalTF, snap.EMA20Slope,
		signalTF, snap.EMA50Slope,
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

	if pUp > t.cfg.BuyThreshold {
		base.Signal = Buy
		base.Raw = Buy
		base.Confidence = pUp
		return base
	}

	if pUp < t.cfg.SellThreshold {
		base.Signal = Sell
		base.Raw = Sell
		base.Confidence = 1 - pUp
		return base
	}

	base.Signal = Flat
	base.Raw = Flat
	return base
}

func (t *Trader) applyMACDSlopeGate(d Decision, execHistory []Candle) Decision {
	if !t.cfg.UseMACDSlopeGate {
		return d
	}

	if len(execHistory) < 60 {
		log.Printf(
			"[MACD_GATE] skip insufficient_history len=%d gateTF=%s",
			len(execHistory),
			t.cfg.Granularity,
		)
		return d
	}

	if d.Signal != Buy && d.Signal != Sell {
		snap, ok := BuildFeatureSnapshot(execHistory, len(execHistory)-1)
		if !ok {
			log.Printf(
				"[MACD_GATE] skip no_feature_snapshot len=%d gateTF=%s",
				len(execHistory),
				t.cfg.Granularity,
			)
			return d
		}
		log.Printf(
			"[MACD_GATE] gateTF=%s raw=%s macdHist_1m=%.5f macdDelta_1m=%.5f eps=%.8f | MACD_Gate_Not_Applied",
			t.cfg.Granularity,
			d.Raw,
			snap.MACDHist,
			snap.MACDHistDelta,
			t.cfg.MACDSlopeEPS,
		)
		return d
	}

	snap, ok := BuildFeatureSnapshot(execHistory, len(execHistory)-1)
	if !ok {
		log.Printf(
			"[MACD_GATE] skip no_feature_snapshot len=%d gateTF=%s",
			len(execHistory),
			t.cfg.Granularity,
		)
		return d
	}

	eps := t.cfg.MACDSlopeEPS
	raw := d.Signal
	reason := ""

	switch d.Signal {

	case Sell:
		if snap.MACDHistDelta > eps {
			d.Signal = Flat
			reason = "bullish_macd_delta_against_sell"
		}

	case Buy:
		if snap.MACDHistDelta < -eps {
			d.Signal = Flat
			reason = "bearish_macd_delta_against_buy"
		}
	}

	d.Reason = appendReason(d.Reason, reason)

	log.Printf(
		"[MACD_GATE] gateTF=%s raw=%s final=%s macdHist_1m=%.5f macdDelta_1m=%.5f eps=%.8f reason=%s",
		t.cfg.Granularity,
		raw,
		d.Signal,
		snap.MACDHist,
		snap.MACDHistDelta,
		eps,
		reason,
	)

	return d
}

func (t *Trader) applyMAFilterGate(d Decision, execHistory []Candle) Decision {
	if !t.cfg.UseMAFilter {
		return d
	}

	if len(execHistory) < 60 {
		return d
	}

	if d.Signal != Buy && d.Signal != Sell {
		snap, ok := BuildFeatureSnapshot(execHistory, len(execHistory)-1)
		if !ok {
			log.Printf(
				"[MA_GATE] skip no_feature_snapshot len=%d gateTF=%s",
				len(execHistory),
				t.cfg.Granularity,
			)
			return d
		}
		log.Printf(
			"[MA_GATE] gateTF=%s raw=%s lowBottom=%v priceDownGoingUp=%v highPeak=%v priceUpGoingDown=%v | MA_Gate_Not_Applied",
			t.cfg.Granularity,
			d.Raw,
			snap.LowBottom,
			snap.PriceDownGoingUp,
			snap.HighPeak,
			snap.PriceUpGoingDown,
		)
		return d
	}

	snap, ok := BuildFeatureSnapshot(execHistory, len(execHistory)-1)
	if !ok {
		log.Printf(
			"[MA_GATE] skip no_feature_snapshot len=%d gateTF=%s",
			len(execHistory),
			t.cfg.Granularity,
		)
		return d
	}

	buyMASignal := snap.LowBottom || snap.PriceDownGoingUp
	sellMASignal := snap.HighPeak || snap.PriceUpGoingDown

	raw := d.Signal
	reason := ""

	if d.Signal == Buy && !buyMASignal {
		d.Signal = Flat
		reason = "ma_gate_block_buy"
	}

	if d.Signal == Sell && !sellMASignal {
		d.Signal = Flat
		reason = "ma_gate_block_sell"
	}

	d.Reason = appendReason(d.Reason, reason)

	log.Printf(
		"[MA_GATE] gateTF=%s raw=%s final=%s buyMA=%v sellMA=%v lowBottom=%v priceDownGoingUp=%v highPeak=%v priceUpGoingDown=%v reason=%s",
		t.cfg.Granularity,
		raw,
		d.Signal,
		buyMASignal,
		sellMASignal,
		snap.LowBottom,
		snap.PriceDownGoingUp,
		snap.HighPeak,
		snap.PriceUpGoingDown,
		reason,
	)

	return d
}

func appendReason(base, reason string) string {
	if reason == "" {
		return base
	}
	if base == "" {
		return reason
	}
	return base + " | " + reason
}

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
//   • Preserve execution/gate logic outside the model decision path
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
	Raw        Signal
	Confidence float64
	Reason     string

	// Carry raw pUp and selected soft-gate flags for downstream gate-audit strings.
	PUp       float64
	HighPeak  bool
	LowBottom bool
	MACDHist  float64
	MACDHistDelta   float64
	MACDHistDeltaSmooth   float64
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
			Raw:        Flat,
		}
	}

	idx := len(signalHistory) - 1
	snap, ok := BuildFeatureSnapshot(signalHistory, idx, t.cfg.MACDLineEPS, t.cfg.AIFeatureDim)
	if !ok {
		return Decision{
			Signal:     Flat,
			Confidence: 0,
			Reason:     "not_enough_features",
			PUp:        0.0,
			Raw:        Flat,
		}
	}

	pUp := 0.5
	if t.model != nil {
		pUp = t.model.Predict(snap.X)
	}

	signalTF := t.cfg.SignalTF()

	// Keep the reason string aligned with the 17-feature hybrid architecture:
	// price/volatility + range + MACD line/hist/slope + restored EMA structure.
	reason := fmt.Sprintf(
		"pUp=%.5f, highPeak_%s=%t, lowBottom_%s=%t, priceDownGoingUp_%s=%t, priceUpGoingDown_%s=%t, distHighPct_%s=%.6f, distLowPct_%s=%.6f, macdLine_%s=%.5f, macdHist_%s=%.5f, macdHistDelta_%s=%.5f, macdHistDeltaSmooth_%s=%.5f, emaSpreadPct_%s=%.6f, ema2050Spread_%s=%.6f, ema20Slope_%s=%.6f, ema50Slope_%s=%.6f",
		pUp,
		signalTF, snap.HighPeak,
		signalTF, snap.LowBottom,
		signalTF, snap.PriceDownGoingUp,
		signalTF, snap.PriceUpGoingDown,
		signalTF, snap.DistHighPct,
		signalTF, snap.DistLowPct,
		signalTF, snap.MACDLine,
		signalTF, snap.MACDHist,
		signalTF, snap.MACDHistDelta,
		signalTF, snap.MACDHistDeltaSmooth,
		signalTF, snap.EMASpreadPct,
		signalTF, snap.EMA2050Spread,
		signalTF, snap.EMA20Slope,
		signalTF, snap.EMA50Slope,
	)

	base := Decision{
		Confidence: 0.5,
		Reason:     reason,
		PUp:        pUp,
		HighPeak:   snap.HighPeak,
		LowBottom:  snap.LowBottom,
		MACDHist:   snap.MACDHist,
		MACDHistDelta: snap.MACDHistDelta,
		MACDHistDeltaSmooth: snap.MACDHistDeltaSmooth,
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
			t.cfg.GateTF,
		)
		return d
	}

	if d.Signal != Buy && d.Signal != Sell {
		snap, ok := BuildFeatureSnapshot(execHistory, len(execHistory)-1, t.cfg.MACDLineEPS, t.cfg.AIFeatureDim )
		if !ok {
			log.Printf(
				"[MACD_GATE] skip no_feature_snapshot len=%d gateTF=%s",
				len(execHistory),
				t.cfg.GateTF,
			)
			return d
		}
		log.Printf(
			"[MACD_GATE] gateTF=%s raw=%s macdLine_%s=%.5f macdTurning_%s=%.5f macdHist_%s=%.5f macdHistDelta_%s=%.5f macdHistDeltaSmooth_%s=%.5f emaSpreadPct_%s=%.6f ema2050Spread_%s=%.6f eps=%.8f | MACD_Gate_Not_Applied",
			t.cfg.GateTF,
			d.Raw,
			t.cfg.GateTF, snap.MACDLine,
			t.cfg.GateTF, snap.MACDTurningPoint,
			t.cfg.GateTF, snap.MACDHist,
			t.cfg.GateTF, snap.MACDHistDelta,
			t.cfg.GateTF, snap.MACDHistDeltaSmooth,
			t.cfg.GateTF, snap.EMASpreadPct,
			t.cfg.GateTF, snap.EMA2050Spread,
			t.cfg.MACDLineEPS,
		)
		return d
	}

	snap, ok := BuildFeatureSnapshot(execHistory, len(execHistory)-1, t.cfg.MACDLineEPS, t.cfg.AIFeatureDim)
	if !ok {
		log.Printf(
			"[MACD_GATE] skip no_feature_snapshot len=%d gateTF=%s",
			len(execHistory),
			t.cfg.GateTF,
		)
		return d
	}

	// Gate remains execution-timeframe based. Do not feed signalHistory here.
	eps := t.cfg.MACDLineEPS
	reason := ""

	switch d.Signal {
	case Sell:
		// Require strong positive MACD regime plus rollover/top pattern.
		if snap.MACDTurningPoint <= eps {
			d.Signal = Flat
			reason = appendReason(reason, "macd_not_strong_positive_for_sell")
		}
		if !snap.HighPeak {
			d.Signal = Flat
			reason = appendReason(reason, "macd_not_high_peak_for_sell")
		}

	case Buy:
		// Require strong negative MACD regime plus bottom reversal pattern.
		if snap.MACDTurningPoint >= -eps {
			d.Signal = Flat
			reason = appendReason(reason, "macd_not_strong_negative_for_buy")
		}
		if !snap.LowBottom {
			d.Signal = Flat
			reason = appendReason(reason, "macd_not_low_bottom_for_buy")
		}
	}

	d.Reason = appendReason(d.Reason, reason)

	log.Printf(
		"[MACD_GATE] gateTF=%s raw=%s final=%s macdLine_%s=%.5f macdTurning_%s=%.5f macdHist_%s=%.5f macdHistDelta_%s=%.5f macdHistDeltaSmooth_%s=%.5f emaSpreadPct_%s=%.6f ema2050Spread_%s=%.6f eps=%.5f highPeak=%t lowBottom=%t reason=%s",
		t.cfg.GateTF,
		d.Raw,
		d.Signal,
		t.cfg.GateTF, snap.MACDLine,
		t.cfg.GateTF, snap.MACDTurningPoint,
		t.cfg.GateTF, snap.MACDHist,
		t.cfg.GateTF, snap.MACDHistDelta,
		t.cfg.GateTF, snap.MACDHistDeltaSmooth,
		t.cfg.GateTF, snap.EMASpreadPct,
		t.cfg.GateTF, snap.EMA2050Spread,
		eps,
		snap.HighPeak,
		snap.LowBottom,
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

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
	PUp float64
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

// decide computes a tradi]ng decision from signal timeframe candles and the unified model.
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

	base := Decision{
		Confidence: 0.5,
		PUp:        pUp,
	}

	if pUp > t.cfg.BuyThreshold {
		base.Signal = Buy
		base.Raw = Buy
		base.Confidence = pUp
		return base
	}else if pUp < t.cfg.SellThreshold {
		base.Signal = Sell
		base.Raw = Sell
		base.Confidence = 1 - pUp
		return base
	}else{
		base.Signal = Flat
		base.Raw = Flat
	}

	lateSellExhaustion := false
	if base.Raw == Sell && snap.MACDTurningPoint <= -120 && snap.DistLowPct <= 0.002 {
		lateSellExhaustion = true
	}

	signalTF := t.cfg.SignalTF()
	reason := fmt.Sprintf(
		"[AI_GATE] signalTF=%s pUp=%.5f exhaustion{lateSell=%v}"+
			"range{high=%.4f low=%.4f} "+
			"macd{line=%.2f turn=%.5f hist=%.2f dHist=%.2f dSmooth=%.2f} "+
			"ema{spread=%.5f ema2050=%.5f slope20=%.5f slope50=%.5f} "+
			"pattern{emaHighPeak=%t emaLowBottom=%t emaDownUp=%t emaUpDown=%t}",

		signalTF,
		pUp,
		lateSellExhaustion,
		snap.DistHighPct,
		snap.DistLowPct,

		snap.MACDLine,
		snap.MACDTurningPoint,
		snap.MACDHist,
		snap.MACDHistDelta,
		snap.MACDHistDeltaSmooth,

		snap.EMASpreadPct,
		snap.EMA2050Spread,
		snap.EMA20Slope,
		snap.EMA50Slope,

		snap.EMAHighPeak,
		snap.EMALowBottom,
		snap.EMAPriceDownGoingUp,
		snap.EMAPriceUpGoingDown,
	)
	
	base.Reason = reason

	return base
}

func (t *Trader) applyLogicGate(d Decision, execHistory []Candle) Decision {
	
	reason := ""

	if !t.cfg.UseMACDSlopeGate {
		return d
	}

	if len(execHistory) < 60 {
		reason = fmt.Sprintf(
			"[LOGIC_GATE] skip insufficient_history len=%d gateTF=%s",
			len(execHistory),
			t.cfg.GateTF,
		)
		d.Reason = appendReason(d.Reason, reason)
		return d
	}

	snap, ok := BuildFeatureSnapshot(execHistory, len(execHistory)-1, t.cfg.MACDLineEPS, t.cfg.AIFeatureDim)
	if !ok {
		reason = fmt.Sprintf(
			"[LOGIC_GATE] skip no_feature_snapshot len=%d gateTF=%s",
			len(execHistory),
			t.cfg.GateTF,
		)
		d.Reason = appendReason(d.Reason, reason)
		return d
	}

	// Gate remains execution-timeframe based. Do not feed signalHistory here.
	emaSellPattern := snap.EMAHighPeak || snap.EMAPriceUpGoingDown
	emaBuyPattern := snap.EMALowBottom || snap.EMAPriceDownGoingUp

	logicOpinion := Flat

	if snap.MACDStrongNegative && snap.MACDMomentumUp && emaBuyPattern {
		logicOpinion = Buy
	} else if snap.MACDStrongPositive && snap.MACDMomentumDown && emaSellPattern {
		logicOpinion = Sell
	}

	logicDisagreement :=
	(d.Raw == Buy && logicOpinion == Sell) ||
	(d.Raw == Sell && logicOpinion == Buy)

	if logicDisagreement {
		d.Signal = Flat
		reason = appendReason(reason, "logic_disagreement")
	}

	switch d.Signal {
	case Sell:
		// 1. Must have strong MACD turn-origin evidence
		if !snap.MACDStrongPositive {
			d.Signal = Flat
			reason = appendReason(reason, "macd_not_strong_positive_for_sell")
		}

		// 2. Must have weakening momentum
		if !snap.MACDMomentumDown {
			d.Signal = Flat
			reason = appendReason(reason, "macd_not_momentum_down_for_sell")
		}

		// 3. Need at least ONE EMA exhaustion pattern
		if !emaSellPattern {
			d.Signal = Flat
			reason = appendReason(reason,
				"ema_no_sell_exhaustion_pattern")
		}

	case Buy:
		// Must have strong MACD negative turn-origin
		if !snap.MACDStrongNegative {
			d.Signal = Flat
			reason = appendReason(reason,
				"macd_not_strong_negative_for_buy")
		}

		// Must have improving momentum
		if !snap.MACDMomentumUp {
			d.Signal = Flat
			reason = appendReason(reason,
				"macd_not_momentum_up_for_buy")
		}

		// Need at least one EMA recovery pattern
		if !emaBuyPattern {

			d.Signal = Flat
			reason = appendReason(reason,
				"ema_no_buy_recovery_pattern")
		}
	default:
		reason = appendReason(reason,
				fmt.Sprintf("ai_%s_logicOpinion=%s", d.Raw, logicOpinion))
	}

	reason = fmt.Sprintf(
		"[LOGIC_GATE] gateTF=%s aiRaw=%s logicOpinion=%s logicDisagreement=%v final=%s | "+
			"MACD{line=%.5f turn=%.5f hist=%.5f dHist=%.5f dSmooth=%.5f} | "+
			"EMA{spread=%.6f ema2050=%.6f} | "+
			"Pattern{emaHighPeak=%v emaLowBottom=%v emaPriceDownGoingUp=%v emaPriceUpGoingDown=%v emaSellPattern=%v emaBuyPattern=%v macdMomentumDown=%v macdMomentumUp=%v macdStrongPositive=%v macdStrongNegative=%v} | "+
			"Gate{eps=%.5f blocked=%v}",

		t.cfg.GateTF,
		d.Raw,
		logicOpinion,
		logicDisagreement,	
		d.Signal,

		snap.MACDLine,
		snap.MACDTurningPoint,
		snap.MACDHist,
		snap.MACDHistDelta,
		snap.MACDHistDeltaSmooth,

		snap.EMASpreadPct,
		snap.EMA2050Spread,

		snap.EMAHighPeak,
		snap.EMALowBottom,
		snap.EMAPriceDownGoingUp,
		snap.EMAPriceUpGoingDown,
		emaSellPattern,
		emaBuyPattern,
		snap.MACDMomentumDown,
		snap.MACDMomentumUp,
		snap.MACDStrongPositive,
		snap.MACDStrongNegative,

		t.cfg.MACDLineEPS,
		reason,
	)

	d.Reason = appendReason(d.Reason, reason)

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

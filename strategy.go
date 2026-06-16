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
	"math"
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

	buyThreshold := t.cfg.BuyThreshold
	sellThreshold := t.cfg.SellThreshold
	if t.model != nil {
		if t.model.BuyThreshold > 0 {
			buyThreshold = t.model.BuyThreshold
		}
		if t.model.SellThreshold > 0 {
			sellThreshold = t.model.SellThreshold
		}
	}

	if pUp <= buyThreshold {
		base.Signal = Buy
		base.Raw = Buy
		base.Confidence = confidenceRiskMultiplier(
			Buy,
			pUp,
			buyThreshold,
			sellThreshold,
		)

	} else if pUp >= sellThreshold {
		base.Signal = Sell
		base.Raw = Sell
		base.Confidence = confidenceRiskMultiplier(
			Sell,
			pUp,
			buyThreshold,
			sellThreshold,
		)

	} else {
		base.Signal = Flat
		base.Raw = Flat
		base.Confidence = 0
	}

	signalTF := t.cfg.SignalTF()
	reason := fmt.Sprintf(
		"[AI_GATE] signalTF=%s pUp=%.5f "+
			"range{high=%.4f low=%.4f} "+
			"macd{line=%.2f turn=%.5f hist=%.2f dHist=%.2f dSmooth=%.2f} "+
			"ema{spread=%.5f ema2050=%.5f slope20=%.5f slope50=%.5f} "+
			"pattern{emaHighPeak=%t emaLowBottom=%t emaDownUp=%t emaUpDown=%t}",

		signalTF,
		pUp,
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

	confidence := d.Confidence

	if confidence < 0.20 {
		confidence = 0.20
	}
	if confidence > 1.00 {
		confidence = 1.00
	}

	epsFactor := 1.20 - confidence
	eps := t.cfg.MACDLineEPS * epsFactor

	if eps < 10 {
		eps = 10
	}

	snap, ok := BuildFeatureSnapshot(execHistory, len(execHistory)-1, eps, t.cfg.AIFeatureDim)
	if !ok {
		reason = fmt.Sprintf(
			"[LOGIC_GATE] skip no_feature_snapshot len=%d gateTF=%s",
			len(execHistory),
			t.cfg.GateTF,
		)
		d.Reason = appendReason(d.Reason, reason)
		return d
	}

	if t.latchedGateBuy > 0 && t.RecentLow > t.latchedGateBuy {
		t.BuyGateTouchedAt = time.Time{}
	}
	if t.latchedGateSell > 0 && t.RecentHigh < t.latchedGateSell {
		t.SellGateTouchedAt = time.Time{}
	}

	buyGateTouched := t.RecentLow > 0 && t.latchedGateBuy > 0 && t.RecentLow <= t.latchedGateBuy
	sellGateTouched := t.RecentHigh > 0 && t.latchedGateSell > 0 && t.RecentHigh >= t.latchedGateSell

	if buyGateTouched && t.BuyGateTouchedAt.IsZero() {
		t.BuyGateTouchedAt = time.Now()
	}
	if sellGateTouched && t.SellGateTouchedAt.IsZero() {
		t.SellGateTouchedAt = time.Now()
	}

	emaSellPattern := snap.EMAHighPeak || snap.EMAPriceUpGoingDown
	emaBuyPattern := snap.EMALowBottom || snap.EMAPriceDownGoingUp

	buyTouchAge := time.Duration(0)
	if !t.BuyGateTouchedAt.IsZero() {
		buyTouchAge = time.Since(t.BuyGateTouchedAt)
	}

	sellTouchAge := time.Duration(0)
	if !t.SellGateTouchedAt.IsZero() {
		sellTouchAge = time.Since(t.SellGateTouchedAt)
	}

	softEPS := eps * 0.40
	if softEPS < 10 {
		softEPS = 10
	}

	softMACDNeg := snap.MACDLine <= -softEPS
	softMACDPos := snap.MACDLine >= softEPS

	normalBuy := snap.MACDStrongNegative && snap.MACDMomentumUp && emaBuyPattern
	softAboveStrongBuy := d.Confidence >= 0.60 && softMACDNeg && snap.MACDMomentumUp && emaBuyPattern
	softenedPostTouchBuy := buyGateTouched && buyTouchAge >= time.Hour && buyTouchAge < time.Hour*2 && softMACDNeg && snap.MACDMomentumUp && emaBuyPattern
	loosePostTouchBuy := buyGateTouched && buyTouchAge >= time.Hour*2 && snap.MACDMomentumUp && snap.EMALowBottom
	loosestPostTouchBuy := buyGateTouched && buyTouchAge >= time.Hour*2 && snap.MACDMomentumUp && emaBuyPattern

	normalSell := snap.MACDStrongPositive && snap.MACDMomentumDown && emaSellPattern
	softAboveStrongSell := d.Confidence >= 0.60 && softMACDPos && snap.MACDMomentumDown && emaSellPattern
	softenedPostTouchSell := sellGateTouched && sellTouchAge >= time.Hour && sellTouchAge < time.Hour*2 && softMACDPos && snap.MACDMomentumDown && emaSellPattern
	loosePostTouchSell := sellGateTouched && sellTouchAge >= time.Hour*2 && snap.MACDMomentumDown && snap.EMAHighPeak
	loosestPostTouchSell := sellGateTouched && sellTouchAge >= time.Hour*3 && snap.MACDMomentumDown && emaSellPattern

	logicOpinion := Flat
	if normalBuy || softAboveStrongBuy || softenedPostTouchBuy || loosePostTouchBuy || loosestPostTouchBuy {
		logicOpinion = Buy
	} else if normalSell || softAboveStrongSell || softenedPostTouchSell || loosePostTouchSell || loosestPostTouchSell {
		logicOpinion = Sell
	}

	logicDisagreement := (d.Raw == Buy && logicOpinion == Sell) || (d.Raw == Sell && logicOpinion == Buy)

	// Final entry signal policy:
	//
	// AI BUY + logic BUY   → BUY
	// AI FLAT + logic BUY  → FLAT
	// AI SELL + logic BUY  → FLAT
	//
	// AI SELL + logic SELL → SELL
	// AI FLAT + logic SELL → FLAT
	// AI BUY + logic SELL  → FLAT
	//
	// logicOpinion already represents the MACD/EMA reversal logic,
	// so we no longer hard-block d.Signal separately below.
	final := finalSignalFromAILogic(d.Raw, logicOpinion)
	d.Signal = final

	if logicDisagreement {
		reason = appendReason(reason, "logic_disagreement")
	}

	if d.Raw == Flat && logicOpinion != Flat {
		reason = appendReason(reason, fmt.Sprintf("ai_FLAT_logicOpinion=%s_blocked", logicOpinion))
	}

	if d.Raw == logicOpinion && logicOpinion != Flat {
		reason = appendReason(reason, fmt.Sprintf("ai_%s_logicOpinion=%s_match", d.Raw, logicOpinion))
	}

	if logicOpinion == Flat {
		reason = appendReason(reason, fmt.Sprintf("ai_%s_logicOpinion=FLAT", d.Raw))
	}

	log.Printf(
		"[KPI] logic ai=%s logic=%s final=%s pUp=%.5f",
		d.Raw,
		logicOpinion,
		d.Signal,
		d.PUp,
	)

	modelUpAvg := 0.0
	modelDownAvg := 0.0

	if t.model != nil {
		if t.model.AvgUp > 0 {
			modelUpAvg = t.model.AvgUp
		}
		if t.model.AvgDown > 0 {
			modelDownAvg = t.model.AvgDown
		}
	}

	reason = appendReason(reason, fmt.Sprintf("softStrongBuy=%v softStrongSell=%v", softAboveStrongBuy, softAboveStrongSell))

	reason = fmt.Sprintf(
		"[LOGIC_GATE] gateTF=%s aiRaw=%s logicOpinion=%s logicDisagreement=%v final=%s | "+
			"MACD{line=%.5f turn=%.5f hist=%.5f dHist=%.5f dSmooth=%.5f} | "+
			"EMA{spread=%.6f ema2050=%.6f} | "+
			"Pattern{emaHighPeak=%v emaLowBottom=%v emaPriceDownGoingUp=%v emaPriceUpGoingDown=%v emaSellPattern=%v emaBuyPattern=%v macdMomentumDown=%v macdMomentumUp=%v macdStrongPositive=%v macdStrongNegative=%v} | "+
			"Gate{confidence=%.2f epsFactor=%.2f eps=%.5f note=%v} modelUpAvg=%.5f modelDownAvg=%.5f",

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
		confidence,
		epsFactor,
		eps,
		reason,
		modelUpAvg,
		modelDownAvg,
	)

	d.Reason = appendReason(d.Reason, reason)

	return d
}

func finalSignalFromAILogic(aiRaw Signal, logicOpinion Signal) Signal {
	if logicOpinion == Flat {
		return Flat
	}

	if aiRaw == Flat {
		return Flat
	}

	if aiRaw == logicOpinion {
		return logicOpinion
	}

	return Flat
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

func confidenceRiskMultiplier(sig Signal, pUp, buyThreshold, sellThreshold float64) float64 {
	const (
		minConf    = 0.20
		maxConf    = 1.00
		sellStrong = 0.70
		buyStrong  = 0.20
		curve      = 1.50 // >1 = stricter near threshold, stronger only when farther away
	)

	switch sig {
	case Buy:
		if pUp > buyThreshold {
			return 0.00
		}
		if pUp <= buyStrong {
			return maxConf
		}

		x := (buyThreshold - pUp) / (buyThreshold - buyStrong)
		x = math.Pow(clamp01(x), curve)
		return minConf + x*(maxConf-minConf)

	case Sell:
		if pUp < sellThreshold {
			return 0.00
		}
		if pUp >= sellStrong {
			return maxConf
		}

		x := (pUp - sellThreshold) / (sellStrong - sellThreshold)
		x = math.Pow(clamp01(x), curve)
		return minConf + x*(maxConf-minConf)
	}

	return 0.00
}

func clamp01(x float64) float64 {
	if x < 0 {
		return 0
	}
	if x > 1 {
		return 1
	}
	return x
}

func shouldExitByAILogic(lot *Position, d Decision) bool {
	if lot.Side == SideBuy {
		return d.Signal == Sell
	}
	if lot.Side == SideSell {
		return d.Signal == Buy
	}
	return false
}

// lowestLow returns the lowest candle low within the rolling lookback window.
// If no candle falls inside the window, returns 0.
//
// Example:
//
//	lowestLow(execHistory, 4*time.Hour)
func lowestLow(candles []Candle, lookback time.Duration) float64 {
	if len(candles) == 0 || lookback <= 0 {
		return 0
	}

	latest := candles[len(candles)-1].Time
	if latest.IsZero() {
		latest = time.Now().UTC()
	}

	cutoff := latest.Add(-lookback)

	lowest := 0.0
	found := false

	for i := len(candles) - 1; i >= 0; i-- {
		c := candles[i]

		// stop once outside window
		if !c.Time.IsZero() && c.Time.Before(cutoff) {
			break
		}

		if !found || c.Low < lowest {
			lowest = c.Low
			found = true
		}
	}

	if !found {
		return 0
	}

	return lowest
}

// highestHigh returns the highest candle high within the rolling lookback window.
// If no candle falls inside the window, returns 0.
//
// Example:
//
//	highestHigh(execHistory, 4*time.Hour)
func highestHigh(candles []Candle, lookback time.Duration) float64 {
	if len(candles) == 0 || lookback <= 0 {
		return 0
	}

	latest := candles[len(candles)-1].Time
	if latest.IsZero() {
		latest = time.Now().UTC()
	}

	cutoff := latest.Add(-lookback)

	highest := 0.0
	found := false

	for i := len(candles) - 1; i >= 0; i-- {
		c := candles[i]

		// stop once outside window
		if !c.Time.IsZero() && c.Time.Before(cutoff) {
			break
		}

		if !found || c.High > highest {
			highest = c.High
			found = true
		}
	}

	if !found {
		return 0
	}

	return highest
}

func confidenceEffPctMultiplier(confidence float64) float64 {
	const (
		minGateMult = 0.20 // strongest confidence
		maxGateMult = 1.00 // weakest confidence
		curve       = 1.50 // smoothness
	)

	// confidence expected in [0.20, 1.00]
	x := (confidence - 0.20) / 0.80
	x = clamp01(x)

	// optional curve
	x = math.Pow(x, curve)

	// invert: stronger confidence => smaller multiplier
	return maxGateMult - x*(maxGateMult-minGateMult)
}

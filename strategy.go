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

	// Model / AI summary
	PUp           float64
	BuyThreshold  float64
	SellThreshold float64
	ModelUpAvg    float64
	ModelDownAvg  float64

	// Logic summary
	LogicOpinion Signal

	// Logic MACD raw materials
	LogicMACDLine           float64
	LogicMACDTurn           float64
	LogicMACDHist           float64
	LogicMACDDHist          float64
	LogicMACDDSmooth        float64
	LogicMACDStrongPositive bool
	LogicMACDStrongNegative bool
	LogicMACDMomentumDown   bool
	LogicMACDMomentumUp     bool

	// Logic EMA raw materials
	LogicEMASpread float64
	LogicEMA2050   float64

	// Logic pattern raw materials
	LogicPatternHighPeak    bool
	LogicPatternLowBottom   bool
	LogicPatternPriceDownUp bool
	LogicPatternPriceUpDown bool
	LogicPatternBuy         bool
	LogicPatternSell        bool

	// Stop-loss / exit context
	Side             OrderSide
	ExitReason       string
	ExitClass        string
	PreviousAIRaw    Signal
	StopLossPNLUSD   float64
	StopLossLimitUSD float64
	ExitNetPNLUSD    float64
	LogicEPS         float64
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
	return base
}

func (t *Trader) applyLogicGate(d Decision, execHistory []Candle) Decision {

	if !t.cfg.UseMACDSlopeGate {
		return d
	}

	if len(execHistory) < 60 {
		log.Fatalf(
			"[LOGIC_GATE] skip insufficient_history len=%d gateTF=%s",
			len(execHistory),
			t.cfg.GateTF,
		)
		return d
	}

	confidence := d.Confidence
	epsMult := confidenceEffPctMultiplier(confidence)
	eps := t.cfg.MACDLineEPS * epsMult
	if eps < 10 {
		eps = 10
	}

	snap, ok := BuildFeatureSnapshot(execHistory, len(execHistory)-1, eps, t.cfg.AIFeatureDim)
	if !ok {
		log.Fatalf(
			"[LOGIC_GATE] skip no_feature_snapshot len=%d gateTF=%s",
			len(execHistory),
			t.cfg.GateTF,
		)
		return d
	}

	emaSellPattern := snap.EMAHighPeak || snap.EMAPriceUpGoingDown
	emaBuyPattern := snap.EMALowBottom || snap.EMAPriceDownGoingUp

	normalBuy := snap.MACDStrongNegative && snap.MACDMomentumUp && emaBuyPattern

	normalSell := snap.MACDStrongPositive && snap.MACDMomentumDown && emaSellPattern

	logicOpinion := Flat
	if normalBuy {
		logicOpinion = Buy
	} else if normalSell {
		logicOpinion = Sell
	}

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

	log.Printf(
		"[KPI] logic ai=%s logic=%s final=%s pUp=%.5f",
		d.Raw,
		logicOpinion,
		d.Signal,
		d.PUp,
	)

	// Logic summary
	d.LogicOpinion = logicOpinion
	d.Confidence = confidence
	d.LogicEPS = eps

	// Logic MACD
	d.LogicMACDLine = snap.MACDLine
	d.LogicMACDTurn = snap.MACDTurningPoint
	d.LogicMACDHist = snap.MACDHist
	d.LogicMACDDHist = snap.MACDHistDelta
	d.LogicMACDDSmooth = snap.MACDHistDeltaSmooth
	d.LogicMACDStrongPositive = snap.MACDStrongPositive
	d.LogicMACDStrongNegative = snap.MACDStrongNegative
	d.LogicMACDMomentumDown = snap.MACDMomentumDown
	d.LogicMACDMomentumUp = snap.MACDMomentumUp

	// Logic Pattern
	d.LogicPatternHighPeak = snap.EMAHighPeak
	d.LogicPatternLowBottom = snap.EMALowBottom
	d.LogicPatternPriceDownUp = snap.EMAPriceDownGoingUp
	d.LogicPatternPriceUpDown = snap.EMAPriceUpGoingDown
	d.LogicPatternSell = emaSellPattern
	d.LogicPatternBuy = emaBuyPattern

	// Logic EMA
	d.LogicEMASpread = snap.EMASpreadPct
	d.LogicEMA2050 = snap.EMA2050Spread

	return d
}

// Confidence scaling.
//
//	direct relationship with confidence.
//	Higher confidence => larger confidence_mult.
//	Used for AI_FLAT net-profit activation/exit gates
//	(ProfitGateUSD / ActivateGateUSD) EPS logic gates and position sizing.
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

// Confidence scaling.
//
//	inverse relationship with confidence.
//	Higher confidence => smaller effPct and shorter tFloor.
//	Used for pyramid adverse gating, winLow/winHigh collection,
//	latch timing, and latched-gate activation.
func confidenceEffPctMultiplier(confidence float64) float64 {
	const (
		minGateMult = 0.20 // lowest confidence
		maxGateMult = 1.00 // highest confidence
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

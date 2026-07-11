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

type MarketRegime string

const (
	RegimeNormal MarketRegime = "NORMAL"
	RegimeUp     MarketRegime = "UP"
	RegimeDown   MarketRegime = "DOWN"
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
	LogicBaseEPS     float64
	LogicRegimeEPS   float64
	MarketRegime     MarketRegime
	RegimeMult       float64
}

// SignalToSide converts the intent into a broker side.
func (d Decision) SignalToSide() (OrderSide, bool) {
	switch d.Signal {
	case Buy:
		return SideBuy, true
	case Sell:
		return SideSell, true
	default:
		return "", false
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

	base.BuyThreshold = t.cfg.BuyThreshold
	base.SellThreshold = t.cfg.SellThreshold
	if t.model != nil {
		if t.model.BuyThreshold > 0 {
			base.BuyThreshold = t.model.BuyThreshold
		}
		if t.model.SellThreshold > 0 {
			base.SellThreshold = t.model.SellThreshold
		}
	}

	if pUp <= base.BuyThreshold {
		base.Signal = Buy
		base.Raw = Buy
		base.Confidence = confidenceRiskMultiplier(
			Buy,
			pUp,
			base.BuyThreshold,
			base.SellThreshold,
		)

	} else if pUp >= base.SellThreshold {
		base.Signal = Sell
		base.Raw = Sell
		base.Confidence = confidenceRiskMultiplier(
			Sell,
			pUp,
			base.BuyThreshold,
			base.SellThreshold,
		)

	} else {
		base.Signal = Flat
		base.Raw = Flat
		base.Confidence = 0
	}
	return base
}

const trendMult = 0.80

func (t *Trader) applyLogicGate(d Decision, execHistory []Candle) Decision {
	if !t.cfg.UseMACDSlopeGate {
		return d
	}

	if len(execHistory) < 60 {
		log.Fatalf("[LOGIC_GATE] skip insufficient_history len=%d gateTF=%s", len(execHistory), t.cfg.GateTF)
		return d
	}

	baseEPS := t.cfg.MACDLineEPS
	regimeEPS := baseEPS
	regimeMult := t.RegimeMultiplier
	if regimeMult <= 0 {
		regimeMult = 1.0
	}

	switch t.MarketRegime {
	case RegimeDown:
		if d.Raw == Buy {
			// Counter-trend BUY → stricter
			regimeEPS = baseEPS * regimeMult
		}
		if d.Raw == Sell {
			// Trend SELL → easier
			regimeEPS = baseEPS * trendMult * 0.8
		}

	case RegimeUp:
		if d.Raw == Sell {
			// Counter-trend SELL → stricter
			regimeEPS = baseEPS * regimeMult * 0.8
		}
		if d.Raw == Buy {
			// Trend BUY → easier
			regimeEPS = baseEPS * trendMult
		}
	}

	eps := regimeEPS * confidenceEffPctMultiplier(d.Confidence)
	if eps < 10 {
		eps = 10
	}

	snap, ok := BuildFeatureSnapshot(execHistory, len(execHistory)-1, eps, t.cfg.AIFeatureDim)
	if !ok {
		log.Fatalf("[LOGIC_GATE] skip no_feature_snapshot len=%d gateTF=%s", len(execHistory), t.cfg.GateTF)
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

	d.Signal = finalSignalFromAILogic(d.Raw, logicOpinion)

	d.LogicOpinion = logicOpinion
	d.LogicEPS = eps

	d.LogicMACDLine = snap.MACDLine
	d.LogicMACDTurn = snap.MACDTurningPoint
	d.LogicMACDHist = snap.MACDHist
	d.LogicMACDDHist = snap.MACDHistDelta
	d.LogicMACDDSmooth = snap.MACDHistDeltaSmooth
	d.LogicMACDStrongPositive = snap.MACDStrongPositive
	d.LogicMACDStrongNegative = snap.MACDStrongNegative
	d.LogicMACDMomentumDown = snap.MACDMomentumDown
	d.LogicMACDMomentumUp = snap.MACDMomentumUp

	d.LogicPatternHighPeak = snap.EMAHighPeak
	d.LogicPatternLowBottom = snap.EMALowBottom
	d.LogicPatternPriceDownUp = snap.EMAPriceDownGoingUp
	d.LogicPatternPriceUpDown = snap.EMAPriceUpGoingDown
	d.LogicPatternSell = emaSellPattern
	d.LogicPatternBuy = emaBuyPattern

	d.LogicEMASpread = snap.EMASpreadPct
	d.LogicEMA2050 = snap.EMA2050Spread
	d.MarketRegime = t.MarketRegime
	d.RegimeMult = regimeMult

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

func (t *Trader) updateMarketRegimeFromRecentExtremes(candles []Candle, wallNow time.Time) {
	t.mu.Lock()
	defer t.mu.Unlock()
	const (
		startMult = 2.0
		stepMult  = 0.25
		maxMult   = 3.0
	)

	t.RecentHigh = highestHigh(candles, 12*time.Hour)
	t.RecentLow = lowestLow(candles, 12*time.Hour)

	freshLow :=
		t.PreviousRecentLow > 0 &&
			t.RecentLow > 0 &&
			t.RecentLow < t.PreviousRecentLow

	freshHigh :=
		t.PreviousRecentHigh > 0 &&
			t.RecentHigh > 0 &&
			t.RecentHigh > t.PreviousRecentHigh

	expiredByTime := !t.RegimeUntil.IsZero() && wallNow.After(t.RegimeUntil)

	if t.MarketRegime == "" {
		t.MarketRegime = RegimeNormal
	}
	if t.RegimeMultiplier <= 0 {
		t.RegimeMultiplier = 1.0
	}

	clampMult := func(v float64) float64 {
		if v > maxMult {
			return maxMult
		}
		if v < 1.0 {
			return 1.0
		}
		return v
	}

	setRegime := func(regime MarketRegime, mult float64, reason string) {
		old := t.MarketRegime
		oldMult := t.RegimeMultiplier

		t.MarketRegime = regime
		t.RegimeMultiplier = clampMult(mult)
		t.RegimeUntil = wallNow.Add(2 * time.Hour)

		log.Printf(
			"[TRACE] regime.set old=%s new=%s reason=%s oldMult=%.2f mult=%.2f recentLow=%.2f previousRecentLow=%.2f recentHigh=%.2f previousRecentHigh=%.2f until=%s",
			old,
			t.MarketRegime,
			reason,
			oldMult,
			t.RegimeMultiplier,
			t.RecentLow,
			t.PreviousRecentLow,
			t.RecentHigh,
			t.PreviousRecentHigh,
			t.RegimeUntil.Format(time.RFC3339),
		)
	}

	extendRegime := func(regime MarketRegime, reason string) {
		oldMult := t.RegimeMultiplier

		t.MarketRegime = regime
		t.RegimeMultiplier = clampMult(t.RegimeMultiplier + stepMult)
		t.RegimeUntil = wallNow.Add(2 * time.Hour)

		log.Printf(
			"[TRACE] regime.extend regime=%s reason=%s oldMult=%.2f mult=%.2f recentLow=%.2f previousRecentLow=%.2f recentHigh=%.2f previousRecentHigh=%.2f until=%s",
			t.MarketRegime,
			reason,
			oldMult,
			t.RegimeMultiplier,
			t.RecentLow,
			t.PreviousRecentLow,
			t.RecentHigh,
			t.PreviousRecentHigh,
			t.RegimeUntil.Format(time.RFC3339),
		)
	}

	toNormal := func(reason string) {
		old := t.MarketRegime
		oldMult := t.RegimeMultiplier

		t.MarketRegime = RegimeNormal
		t.RegimeMultiplier = 1.0
		t.RegimeUntil = time.Time{}

		log.Printf(
			"[TRACE] regime.normal old=%s new=%s reason=%s oldMult=%.2f mult=%.2f recentLow=%.2f previousRecentLow=%.2f recentHigh=%.2f previousRecentHigh=%.2f",
			old,
			t.MarketRegime,
			reason,
			oldMult,
			t.RegimeMultiplier,
			t.RecentLow,
			t.PreviousRecentLow,
			t.RecentHigh,
			t.PreviousRecentHigh,
		)
	}

	if freshLow {
		t.RecentLowBreakAt = wallNow
	}
	if freshHigh {
		t.RecentHighBreakAt = wallNow
	}

	changed := false

	switch t.MarketRegime {
	case RegimeNormal:
		if freshLow {
			setRegime(RegimeDown, startMult, "fresh_12h_low_from_normal")
			changed = true
		} else if freshHigh {
			setRegime(RegimeUp, startMult, "fresh_12h_high_from_normal")
			changed = true
		}

	case RegimeDown:
		if freshLow {
			extendRegime(RegimeDown, "fresh_12h_low_extend_down")
			changed = true
		} else if expiredByTime && freshHigh {
			toNormal("expired_and_fresh_12h_high")
			changed = true
		}

	case RegimeUp:
		if freshHigh {
			extendRegime(RegimeUp, "fresh_12h_high_extend_up")
			changed = true
		} else if expiredByTime && freshLow {
			toNormal("expired_and_fresh_12h_low")
			changed = true
		}
	}

	_ = changed

	buyLots := len(t.book(SideBuy).Lots)
	sellLots := len(t.book(SideSell).Lots)

	elapsedBuyHr := 0.0
	elapsedSellHr := 0.0

	if !t.lastAddBuy.IsZero() {
		elapsedBuyHr = wallNow.Sub(t.lastAddBuy).Hours()
	}
	if !t.lastAddSell.IsZero() {
		elapsedSellHr = wallNow.Sub(t.lastAddSell).Hours()
	}

	untilHr := 0.0
	if !t.RegimeUntil.IsZero() {
		untilHr = t.RegimeUntil.Sub(wallNow).Hours()
	}

	lowAgeHr := 0.0
	highAgeHr := 0.0
	if !t.RecentLowBreakAt.IsZero() {
		lowAgeHr = wallNow.Sub(t.RecentLowBreakAt).Hours()
	}
	if !t.RecentHighBreakAt.IsZero() {
		highAgeHr = wallNow.Sub(t.RecentHighBreakAt).Hours()
	}

	log.Printf(
		"[TRACE] recent.window regime=%s mult=%.2f untilHr=%.2f freshHigh=%t freshLow=%t high=%.2f prevHigh=%.2f low=%.2f prevLow=%.2f highAgeHr=%.2f lowAgeHr=%.2f latchedBuy=%.2f latchedSell=%.2f winLowBuy=%.2f winHighSell=%.2f elapsedBuyHr=%.2f elapsedSellHr=%.2f buyLots=%d sellLots=%d dustBuy=%d dustSell=%d",
		t.MarketRegime,
		t.RegimeMultiplier,
		untilHr,
		freshHigh,
		freshLow,
		t.RecentHigh,
		t.PreviousRecentHigh,
		t.RecentLow,
		t.PreviousRecentLow,
		highAgeHr,
		lowAgeHr,
		t.latchedGateBuy,
		t.latchedGateSell,
		t.winLowBuy,
		t.winHighSell,
		elapsedBuyHr,
		elapsedSellHr,
		buyLots,
		sellLots,
		len(t.dustBuyLots),
		len(t.dustSellLots),
	)
}

func (t *Trader) afterStepStateUpdate(wallNow time.Time, res StepResult) {
	_ = wallNow

	t.mu.Lock()
	defer t.mu.Unlock()

	t.previousAIRaw = res.Raw

	if t.RecentLow > 0 {
		t.PreviousRecentLow = t.RecentLow
	}

	if t.RecentHigh > 0 {
		t.PreviousRecentHigh = t.RecentHigh
	}
}

func (t *Trader) recoveryTargetAddUSD() float64 {
	if t.RecoveryDebtUSD <= 0 {
		return 0
	}

	pct := t.cfg.RecoveryTargetPct
	if pct <= 0 {
		pct = 0.25
	}

	maxAdd := t.cfg.RecoveryMaxAddUSD
	if maxAdd <= 0 {
		maxAdd = 0.50
	}

	add := t.RecoveryDebtUSD * pct
	if add > maxAdd {
		add = maxAdd
	}
	if add < 0 {
		add = 0
	}
	return add
}

func (t *Trader) applyRecoveryDebtFromExit(pnl float64) {
	if pnl < 0 {
		old := t.RecoveryDebtUSD
		t.RecoveryDebtUSD += math.Abs(pnl)

		log.Printf(
			"[TRACE] recovery.loss pnl=%.4f debt_before=%.4f debt_after=%.4f",
			pnl,
			old,
			t.RecoveryDebtUSD,
		)
		return
	}

	if pnl > 0 && t.RecoveryDebtUSD > 0 {
		old := t.RecoveryDebtUSD
		recovered := math.Min(pnl, t.RecoveryDebtUSD)
		t.RecoveryDebtUSD -= recovered
		if t.RecoveryDebtUSD < 0 {
			t.RecoveryDebtUSD = 0
		}

		log.Printf(
			"[TRACE] recovery.profit pnl=%.4f recovered=%.4f debt_before=%.4f debt_after=%.4f",
			pnl,
			recovered,
			old,
			t.RecoveryDebtUSD,
		)
	}
}

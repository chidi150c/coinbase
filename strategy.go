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
	"strings"
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

// ExitDecision contains only the information required to
// execute, classify and audit an exit.
//
// It is intentionally independent of the AI / Logic entry
// decision engine.
type ExitDecision struct {
	Side OrderSide

	// Why we are exiting.
	ExitReason string
	ExitClass  string

	// Financial context.
	ExitNetPNLUSD    float64
	StopLossPNLUSD   float64
	StopLossLimitUSD float64

	// Market context at exit.
	MarketRegime MarketRegime
	RegimeMult   float64
}

func decisionExitReason(d ExitDecision) string {
	parts := []string{
		fmt.Sprintf("side=%s", d.Side),
		fmt.Sprintf("regime=%s", d.MarketRegime),
		fmt.Sprintf("regimeMult=%.2f", d.RegimeMult),
	}

	if strings.TrimSpace(d.ExitReason) != "" {
		parts = append(parts,
			fmt.Sprintf("exitReason=%s", d.ExitReason))
	}

	if strings.TrimSpace(d.ExitClass) != "" {
		parts = append(parts,
			fmt.Sprintf("exitClass=%s", d.ExitClass))
	}

	if d.ExitNetPNLUSD != 0 {
		parts = append(parts,
			fmt.Sprintf("exitNetPNL=%.5f", d.ExitNetPNLUSD))
	}

	if d.StopLossPNLUSD != 0 {
		parts = append(parts,
			fmt.Sprintf("stopLossPNL=%.5f", d.StopLossPNLUSD))
	}

	if d.StopLossLimitUSD != 0 {
		parts = append(parts,
			fmt.Sprintf("stopLossLimit=%.5f", d.StopLossLimitUSD))
	}

	return strings.Join(parts, "|")
}

// EquityRawResult preserves the complete direction-independent Equity
// threshold snapshot.
//
// It does not apply an entry signal, inspect Pyramid, or determine whether
// an order should be submitted.
type EquityRawResult struct {
	// Original state inputs.
	EquityUSD   float64
	BaselineUSD float64

	// Configured threshold multipliers.
	BuyTriggerMult  float64
	SellTriggerMult float64

	// Calculated trigger thresholds.
	BuyTriggerUSD  float64
	SellTriggerUSD float64

	// Change from baseline.
	EquityDeltaUSD  float64
	EquityRatio     float64
	EquityChangePct float64

	// Signed distance from each trigger.
	//
	// BUY:  <= 0 means the BUY threshold passed.
	// SELL: >= 0 means the SELL threshold passed.
	BuyThresholdDistanceUSD  float64
	SellThresholdDistanceUSD float64

	BaselineValid       bool
	BuyThresholdPassed  bool
	SellThresholdPassed bool

	Err     error
	Elapsed time.Duration
}

// EquityResult preserves the raw Equity snapshot, applies the directional
// input supplied by the Equity material evaluator, applies available spare
// funding, and proposes the Equity BUY/SELL trigger.
//
// It does not access balances, enforce LongOnly, check lot caps, or place an
// order.
type EquityResult struct {
	Raw EquityRawResult

	LegacySignal Signal

	// Funding materials supplied by step().
	RawSpareQuote float64
	RawSpareBase  float64
	SpareQuote    float64
	SpareBase     float64
	QuoteStep     float64
	BaseStep      float64

	// Directional threshold applicability.
	BuyApplicable  bool
	SellApplicable bool

	// Funding after exchange-step snapping.
	BuyFundingAvailable  bool
	SellFundingAvailable bool
	ProposedBuyQuote     float64
	ProposedSellBase     float64

	// Proposed Equity routes.
	BuyTrigger  bool
	SellTrigger bool
	Selected    bool

	Reason string

	Err     error
	Elapsed time.Duration
}

type AIResult struct {
	Raw           Signal
	PUp           float64
	BuyThreshold  float64
	SellThreshold float64
	Confidence    float64

	Err     error
	Elapsed time.Duration
}

func (t *Trader) evaluateAI(
	signalHistory []Candle,
) AIResult {

	started := time.Now()

	result := AIResult{}

	defer func() {
		result.Elapsed = time.Since(started)
	}()

	if len(signalHistory) < 60 {
		result.Err = fmt.Errorf(
			"insufficient signal history len=%d",
			len(signalHistory),
		)
		return result
	}

	idx := len(signalHistory) - 1

	snap, ok := BuildFeatureSnapshot(
		signalHistory,
		idx,
		t.cfg.MACDLineEPS, t.cfg.AIFeatureDim,
	)
	if !ok {
		result.Err = fmt.Errorf(
			"AI feature snapshot unavailable len=%d",
			len(signalHistory),
		)
		return result
	}

	pUp := 0.5
	if t.model != nil {
		pUp = t.model.Predict(snap.X)
	}

	result.PUp = pUp

	result.BuyThreshold = t.cfg.BuyThreshold
	result.SellThreshold = t.cfg.SellThreshold

	if t.model != nil {
		if t.model.BuyThreshold > 0 {
			result.BuyThreshold = t.model.BuyThreshold
		}

		if t.model.SellThreshold > 0 {
			result.SellThreshold = t.model.SellThreshold
		}
	}

	switch {

	case pUp <= result.BuyThreshold:

		result.Raw = Buy

		result.Confidence =
			confidenceRiskMultiplier(
				Buy,
				pUp,
				result.BuyThreshold,
				result.SellThreshold,
			)

	case pUp >= result.SellThreshold:

		result.Raw = Sell

		result.Confidence =
			confidenceRiskMultiplier(
				Sell,
				pUp,
				result.BuyThreshold,
				result.SellThreshold,
			)

	default:
		result.Raw = Flat
		result.Confidence = 0
	}

	return result
}

type MACDSnapshotResult struct {
	Line      float64
	LinePrev6 float64
	Turn      float64
	Hist      float64
	DHist     float64
	DSmooth   float64

	// Raw momentum inputs produced by the snapshot.
	MomentumDown bool
	MomentumUp   bool

	Err     error
	Elapsed time.Duration
}

type MACDResult struct {
	Opinion Signal
	EPS     float64

	Line      float64
	LinePrev6 float64
	Turn      float64
	Hist      float64
	DHist     float64
	DSmooth   float64

	StrongPositive bool
	StrongNegative bool
	MomentumDown   bool
	MomentumUp     bool

	Err       error
	Elapsed   time.Duration
	RegimeEPS float64
	BaseEPS   float64
}

type EMAPatternResult struct {
	Opinion Signal

	Spread  float64
	EMA2050 float64

	HighPeak    bool
	LowBottom   bool
	PriceDownUp bool
	PriceUpDown bool
	PatternBuy  bool
	PatternSell bool

	Err     error
	Elapsed time.Duration
}

func (t *Trader) evaluateEMAPatternSnapshot(
	execHistory []Candle,
) EMAPatternResult {

	started := time.Now()

	result := EMAPatternResult{}

	defer func() {
		result.Elapsed = time.Since(started)
	}()

	if len(execHistory) < 60 {
		result.Err = fmt.Errorf(
			"insufficient execution history len=%d gateTF=%s",
			len(execHistory),
			t.cfg.GateTF,
		)
		return result
	}

	// Raw EMA/pattern snapshot only.
	// No AI confidence or MACD EPS required.
	snap, ok := BuildFeatureSnapshot(
		execHistory,
		len(execHistory)-1,
		t.cfg.MACDLineEPS,
		t.cfg.AIFeatureDim,
	)
	if !ok {
		result.Err = fmt.Errorf(
			"EMA feature snapshot unavailable len=%d gateTF=%s",
			len(execHistory),
			t.cfg.GateTF,
		)
		return result
	}

	result.Spread =
		snap.EMASpreadPct

	result.EMA2050 =
		snap.EMA2050Spread

	result.HighPeak =
		snap.EMAHighPeak

	result.LowBottom =
		snap.EMALowBottom

	result.PriceDownUp =
		snap.EMAPriceDownGoingUp

	result.PriceUpDown =
		snap.EMAPriceUpGoingDown

	result.PatternBuy =
		snap.EMALowBottom ||
			snap.EMAPriceDownGoingUp

	result.PatternSell =
		snap.EMAHighPeak ||
			snap.EMAPriceUpGoingDown

	switch {

	case result.PatternBuy && !result.PatternSell:
		result.Opinion = Buy

	case result.PatternSell && !result.PatternBuy:
		result.Opinion = Sell

	default:
		result.Opinion = Flat
	}

	return result
}

// evaluateEquityRaw captures direction-independent Equity threshold evidence.
func (t *Trader) evaluateEquityRaw() EquityRawResult {
	started := time.Now()

	// Raw Equity remains producer-neutral. Continuation admission is applied
	// later by evaluateEquityProducerMaterial() from the standardized
	// producer+side committed-reference snapshot.
	result := EquityRawResult{
		EquityUSD:       t.equityUSD,
		BaselineUSD:     t.lastAddEquity,
		BuyTriggerMult:  t.cfg.BuyEquityTriggerMult,
		SellTriggerMult: t.cfg.SellEquityTriggerMult,
	}

	defer func() {
		result.Elapsed = time.Since(started)
	}()

	result.BaselineValid = result.BaselineUSD > 0
	if !result.BaselineValid {
		return result
	}

	result.BuyTriggerUSD =
		result.BaselineUSD *
			result.BuyTriggerMult

	result.SellTriggerUSD =
		result.BaselineUSD *
			result.SellTriggerMult

	result.EquityDeltaUSD =
		result.EquityUSD -
			result.BaselineUSD

	result.EquityRatio =
		result.EquityUSD /
			result.BaselineUSD

	result.EquityChangePct =
		(result.EquityRatio - 1.0) *
			100.0

	result.BuyThresholdDistanceUSD =
		result.EquityUSD -
			result.BuyTriggerUSD

	result.SellThresholdDistanceUSD =
		result.EquityUSD -
			result.SellTriggerUSD

	result.BuyThresholdPassed =
		result.BuyThresholdDistanceUSD <= 0

	result.SellThresholdPassed =
		result.SellThresholdDistanceUSD >= 0

	return result
}

// interpretEquityRaw applies the supplied Equity-owned direction and available
// spare funding to the direction-independent Equity snapshot.
//
// It proposes an Equity trigger and a step-snapped amount. LongOnly and final
// order validation remain in step().
func interpretEquityRaw(
	raw EquityRawResult,
	legacySignal Signal,
	spareQuote float64,
	spareBase float64,
	quoteStep float64,
	baseStep float64,
) EquityResult {
	started := time.Now()

	result := EquityResult{
		Raw:           raw,
		LegacySignal:  legacySignal,
		RawSpareQuote: spareQuote,
		RawSpareBase:  spareBase,
		QuoteStep:     quoteStep,
		BaseStep:      baseStep,
		Err:           raw.Err,
	}

	defer func() {
		result.Elapsed = time.Since(started)
	}()

	if raw.Err != nil {
		return result
	}

	if spareQuote < 0 {
		spareQuote = 0
	}
	if spareBase < 0 {
		spareBase = 0
	}

	result.SpareQuote = spareQuote
	result.SpareBase = spareBase

	result.BuyApplicable =
		legacySignal == Buy &&
			raw.BaselineValid &&
			raw.BuyThresholdPassed

	result.SellApplicable =
		legacySignal == Sell &&
			raw.BaselineValid &&
			raw.SellThresholdPassed

	if quoteStep > 0 {
		result.ProposedBuyQuote =
			math.Floor(spareQuote/quoteStep) *
				quoteStep
	}

	if baseStep > 0 {
		result.ProposedSellBase =
			math.Floor(spareBase/baseStep) *
				baseStep
	}

	result.BuyFundingAvailable =
		result.ProposedBuyQuote > 0

	result.SellFundingAvailable =
		result.ProposedSellBase > 0

	result.BuyTrigger =
		result.BuyApplicable &&
			result.BuyFundingAvailable

	result.SellTrigger =
		result.SellApplicable &&
			result.SellFundingAvailable

	result.Selected =
		result.BuyTrigger ||
			result.SellTrigger

	result.Reason = fmt.Sprintf(
		"legacy=%s|equity=%.2f|baseline=%.2f|"+
			"buyMult=%.6f|sellMult=%.6f|"+
			"buyTriggerUSD=%.2f|sellTriggerUSD=%.2f|"+
			"buyDistanceUSD=%.2f|sellDistanceUSD=%.2f|"+
			"buyPassed=%t|sellPassed=%t|"+
			"rawSpareQuote=%.8f|rawSpareBase=%.8f|"+
			"spareQuote=%.8f|spareBase=%.8f|"+
			"proposedBuyQuote=%.8f|proposedSellBase=%.8f|"+
			"buyTrigger=%t|sellTrigger=%t",
		legacySignal,
		raw.EquityUSD,
		raw.BaselineUSD,
		raw.BuyTriggerMult,
		raw.SellTriggerMult,
		raw.BuyTriggerUSD,
		raw.SellTriggerUSD,
		raw.BuyThresholdDistanceUSD,
		raw.SellThresholdDistanceUSD,
		raw.BuyThresholdPassed,
		raw.SellThresholdPassed,
		result.RawSpareQuote,
		result.RawSpareBase,
		result.SpareQuote,
		result.SpareBase,
		result.ProposedBuyQuote,
		result.ProposedSellBase,
		result.BuyTrigger,
		result.SellTrigger,
	)

	log.Printf(
		"[DEBUG] EQUITY Trading: equityUSD=%.2f baseline=%.2f BUY trigger at<=%.2f SELL trigger at>=%.2f",
		raw.EquityUSD,
		raw.BaselineUSD,
		raw.BuyTriggerUSD,
		raw.SellTriggerUSD,
	)

	return result
}

func (t *Trader) evaluateMACDSnapshot(
	execHistory []Candle,
) MACDSnapshotResult {

	started := time.Now()

	result := MACDSnapshotResult{}

	defer func() {
		result.Elapsed = time.Since(started)
	}()

	if len(execHistory) < 60 {
		result.Err = fmt.Errorf(
			"insufficient execution history len=%d gateTF=%s",
			len(execHistory),
			t.cfg.GateTF,
		)
		return result
	}

	// Raw 1m snapshot only.
	// No AI confidence or regime-adjusted EPS is needed here.
	snap, ok := BuildFeatureSnapshot(
		execHistory,
		len(execHistory)-1,
		t.cfg.MACDLineEPS,
		t.cfg.AIFeatureDim,
	)
	if !ok {
		result.Err = fmt.Errorf(
			"MACD feature snapshot unavailable len=%d gateTF=%s",
			len(execHistory),
			t.cfg.GateTF,
		)
		return result
	}

	result.Line = snap.MACDLine
	result.LinePrev6 = snap.MACDLinePrev6
	result.Turn = snap.MACDTurningPoint
	result.Hist = snap.MACDHist
	result.DHist = snap.MACDHistDelta
	result.DSmooth = snap.MACDHistDeltaSmooth

	result.MomentumDown = snap.MACDMomentumDown
	result.MomentumUp = snap.MACDMomentumUp

	return result
}

func computeLogicEPS(
	baseEPS float64,
	aiRaw Signal,
	confidence float64,
	regime MarketRegime,
	regimeMult float64,
) (float64, float64, float64) {

	if regimeMult <= 0 {
		regimeMult = 1.0
	}

	regimeEPS := baseEPS

	switch regime {
	case RegimeDown:
		switch aiRaw {
		case Buy:
			// Counter-trend BUY → stricter.
			regimeEPS = baseEPS * regimeMult

		case Sell:
			// Trend SELL → easier.
			regimeEPS = baseEPS * trendMult * 0.8
		}

	case RegimeUp:
		switch aiRaw {
		case Sell:
			// Counter-trend SELL → stricter.
			regimeEPS = baseEPS * regimeMult * 0.8

		case Buy:
			// Trend BUY → easier.
			regimeEPS = baseEPS * trendMult
		}
	}

	eps := regimeEPS * confidenceEffPctMultiplier(confidence)

	if eps < 10 {
		eps = 10
	}

	return eps, regimeEPS, baseEPS
}

func interpretMACD(
	raw MACDSnapshotResult,
	eps float64,
	regimeEPS float64,
	baseEPS float64,
) MACDResult {

	result := MACDResult{
		EPS:          eps,
		Line:         raw.Line,
		LinePrev6:    raw.LinePrev6,
		Turn:         raw.Turn,
		Hist:         raw.Hist,
		DHist:        raw.DHist,
		DSmooth:      raw.DSmooth,
		MomentumDown: raw.MomentumDown,
		MomentumUp:   raw.MomentumUp,
		Err:          raw.Err,
		Elapsed:      raw.Elapsed,
		RegimeEPS:    regimeEPS,
		BaseEPS:      baseEPS,
	}

	if raw.Err != nil {
		return result
	}

	// Use the same exact formulas previously used inside
	// BuildFeatureSnapshot to derive these flags from EPS.

	result.StrongPositive = result.Turn >= result.EPS

	result.StrongNegative = result.Turn <= -result.EPS

	switch {
	case result.StrongNegative && result.MomentumUp:
		result.Opinion = Buy

	case result.StrongPositive && result.MomentumDown:
		result.Opinion = Sell

	default:
		result.Opinion = Flat
	}

	return result
}

//------------------------------------------------------------------------
// 1. Define the Pyramid result structures
//-----------------------------------------------------------------

type PyramidSideRaw struct {
	Side OrderSide

	CurrentPrice float64
	MarketRegime MarketRegime

	// Time and spacing.
	LastAdd     time.Time
	ElapsedSec  float64
	ElapsedMin  float64
	ElapsedHr   float64
	SpacingNeed int
	SpacingPass bool

	// Price anchors.
	LatestEntry           float64
	RecentExtreme         float64
	PreviousRecentExtreme float64
	LastAnchor            float64

	// Configuration/raw decay inputs.
	BasePct     float64
	DecayLambda float64
	DecayFloor  float64

	// Existing Pyramid state.
	WinExtreme float64
	Latched    float64

	// Risk-derived input.
	LatchBufferPrice float64

	// Raw maintenance conditions.
	FreshFavorableExtreme bool
}

// PyramidSideResult preserves the complete original Pyramid raw snapshot
// and adds the confidence-adjusted interpretation derived from it.
//
// Raw is never modified during interpretation. This allows audits and the
// final Case 5 combiner to inspect the exact confidence-independent inputs
// alongside all derived values.
type PyramidSideResult struct {
	// Exact original input supplied to interpretPyramidSideRaw().
	Raw PyramidSideRaw

	Side OrderSide

	CurrentPrice float64

	// Final interpreted conditions.
	SpacingPass bool
	AdversePass bool
	GatePassed  bool

	// Confidence adjustment.
	Confidence float64
	GateMult   float64

	// Final decay/gate values.
	BasePct    float64
	DecayedPct float64
	EffPct     float64

	BaseTFloorMin float64
	TFloorMin     float64
	TFloorHr      float64

	ElapsedSec float64
	ElapsedMin float64
	ElapsedHr  float64

	// Price anchors and final gate.
	LastAnchor         float64
	BaselineGatePrice  float64
	SoftGatePrice      float64
	EffectiveGatePrice float64

	// Existing/proposed latch evidence.
	WinExtreme        float64
	Latched           float64
	LatchBeforeClamp  float64
	LatchAfterClamp   float64
	LatchClampApplied bool

	LatchBufferPrice float64

	// Phase diagnostics.
	ObservingExtreme  bool
	SoftGateEligible  bool
	HardLatchEligible bool
	UsedSoftGate      bool
	UsedLatchedGate   bool

	Reason string
}

// PyramidResult is the interpreted counterpart of PyramidRawResult.
type PyramidResult struct {
	Buy  PyramidSideResult
	Sell PyramidSideResult

	// State transitions produced using the confidence-adjusted timing.
	State PyramidStateTransitions

	Err     error
	Elapsed time.Duration
}

type PyramidSideTransition struct {
	UpdateLastAdd bool
	NextLastAdd   time.Time

	UpdateWin bool
	NextWin   float64

	UpdateLatched bool
	NextLatched   float64

	ElapsedBeforeResetHr float64
}

type PyramidStateTransitions struct {
	Buy  PyramidSideTransition
	Sell PyramidSideTransition

	RebaseSellOnBuy bool
	NextSellWin     float64
	NextSellLatch   float64

	RebaseBuyOnSell bool
	NextBuyWin      float64
	NextBuyLatch    float64
}

type PyramidRawResult struct {
	Buy  PyramidSideRaw
	Sell PyramidSideRaw

	State PyramidStateTransitions

	Err     error
	Elapsed time.Duration
}

// -----------------------------------------------------------------------
// 2. Side evaluator
//   - This helper reproduces the common BUY/SELL calculations without mutating Trader.
//
// --------------------------------------------------------------------------------------
func evaluatePyramidSideRaw(
	side OrderSide,
	price float64,
	wallNow time.Time,
	lastAdd time.Time,
	recentExtreme float64,
	previousRecentExtreme float64,
	winExtreme float64,
	latchedGate float64,
	latestEntry float64,
	marketRegime MarketRegime,
	cfg Config,
) (PyramidSideRaw, PyramidSideTransition) {

	r := PyramidSideRaw{
		Side:                  side,
		CurrentPrice:          price,
		MarketRegime:          marketRegime,
		LastAdd:               lastAdd,
		LatestEntry:           latestEntry,
		RecentExtreme:         recentExtreme,
		PreviousRecentExtreme: previousRecentExtreme,
		WinExtreme:            winExtreme,
		Latched:               latchedGate,
		BasePct:               cfg.PyramidMinAdversePct,
		DecayLambda:           cfg.PyramidDecayLambda,
		DecayFloor:            cfg.PyramidDecayMinPct,
		SpacingNeed:           cfg.PyramidMinSecondsBetween,
		SpacingPass:           true,
	}

	next := PyramidSideTransition{
		NextLastAdd: lastAdd,
		NextWin:     winExtreme,
		NextLatched: latchedGate,
	}

	if !lastAdd.IsZero() {
		elapsed := wallNow.Sub(lastAdd)

		r.ElapsedSec = elapsed.Seconds()
		r.ElapsedMin = elapsed.Minutes()
		r.ElapsedHr = elapsed.Hours()

		r.SpacingPass =
			elapsed >=
				time.Duration(cfg.PyramidMinSecondsBetween)*time.Second
	}

	switch side {
	case SideBuy:
		r.FreshFavorableExtreme =
			latchedGate == 0 &&
				previousRecentExtreme > 0 &&
				recentExtreme > 0 &&
				recentExtreme < previousRecentExtreme

	case SideSell:
		r.FreshFavorableExtreme =
			latchedGate == 0 &&
				previousRecentExtreme > 0 &&
				recentExtreme > 0 &&
				recentExtreme > previousRecentExtreme
	}

	if r.FreshFavorableExtreme {
		next.UpdateLastAdd = true
		next.NextLastAdd = wallNow
		next.ElapsedBeforeResetHr = r.ElapsedHr

		r.LastAdd = wallNow
		r.ElapsedSec = 0
		r.ElapsedMin = 0
		r.ElapsedHr = 0
		r.SpacingPass =
			cfg.PyramidMinSecondsBetween <= 0
	}

	r.LastAnchor = latestEntry

	if r.LastAnchor <= 0 {
		if recentExtreme > 0 {
			r.LastAnchor = recentExtreme
		} else {
			r.LastAnchor = price
		}
	}

	if cfg.RiskPerTradeUSD > 0 && price > 0 {
		fullDistance :=
			math.Abs(cfg.StopLossPnLUSD) *
				price /
				cfg.RiskPerTradeUSD

		r.LatchBufferPrice =
			fullDistance / 4.5
	}

	return r, next
}

// --------------------------------------------------------------------------
// 3. Main Pyramid evaluator
// Stage 1 (evaluatePyramidRaw):
//   - Collects raw BUY and SELL Pyramid observations.
//   - Computes only confidence-independent information.
//   - Detects unconditional maintenance such as LastAdd timer extensions.
//   - Produces candidate state transitions without mutating Trader state.
//
// --------------------------------------------------------------------------
func (t *Trader) evaluatePyramidRaw(
	price float64,
	wallNow time.Time,
) PyramidRawResult {

	started := time.Now()

	result := PyramidRawResult{}

	defer func() {
		result.Elapsed = time.Since(started)
	}()

	result.Buy, result.State.Buy =
		evaluatePyramidSideRaw(
			SideBuy,
			price,
			wallNow,
			t.lastAddBuy,
			t.RecentLow,
			t.PreviousRecentLow,
			t.winLowBuy,
			t.latchedGateBuy,
			t.latestEntryBySide(SideBuy),
			t.MarketRegime,
			t.cfg,
		)

	result.Sell, result.State.Sell =
		evaluatePyramidSideRaw(
			SideSell,
			price,
			wallNow,
			t.lastAddSell,
			t.RecentHigh,
			t.PreviousRecentHigh,
			t.winHighSell,
			t.latchedGateSell,
			t.latestEntryBySide(SideSell),
			t.MarketRegime,
			t.cfg,
		)

	// -------------------------------------------------------------------------
	// Preserve signal-dependent opposite-side rebase as raw candidates.
	//
	// These are not applied yet because the final BUY/SELL decision does not
	// exist at this point.
	// -------------------------------------------------------------------------

	latchResetHours :=
		t.cfg.PyramidLatchResetHours

	if latchResetHours > 0 &&
		t.latchedGateSell > 0 {

		sellLatchAgeHr :=
			wallNow.Sub(t.lastAddSell).Hours()

		if sellLatchAgeHr >= latchResetHours &&
			t.RecentHigh > 0 &&
			t.RecentHigh < t.latchedGateSell {

			result.State.RebaseSellOnBuy = true
			result.State.NextSellLatch =
				t.RecentHigh
			result.State.NextSellWin =
				t.RecentHigh
		}
	}

	if latchResetHours > 0 &&
		t.latchedGateBuy > 0 {

		buyLatchAgeHr :=
			wallNow.Sub(t.lastAddBuy).Hours()

		if buyLatchAgeHr >= latchResetHours &&
			t.RecentLow > 0 &&
			t.RecentLow > t.latchedGateBuy {

			result.State.RebaseBuyOnSell = true
			result.State.NextBuyLatch =
				t.RecentLow
			result.State.NextBuyWin =
				t.RecentLow
		}
	}

	return result
}

// --------------------------------------------------------------------------
// 4. Apply unconditional Pyramid transitions before fan-in
//   - These transitions do not require the final signal:
//
// Applies only confidence-independent Pyramid maintenance.
// Do not update win/latch state here.
// --------------------------------------------------------------------------
func (t *Trader) applyPyramidRawTransitions(
	state PyramidStateTransitions,
) {
	if state.Buy.UpdateLastAdd {
		// log.Printf(
		// 	"[TRACE] pyramid.latch_extend side=BUY recentLow=%.2f prevRecentLow=%.2f elapsedResetAtHr=%.2f",
		// 	t.RecentLow,
		// 	t.PreviousRecentLow,
		// 	state.Buy.ElapsedBeforeResetHr,
		// )

		t.lastAddBuy =
			state.Buy.NextLastAdd
	}

	if state.Sell.UpdateLastAdd {
		// log.Printf(
		// 	"[TRACE] pyramid.latch_extend side=SELL recentHigh=%.2f prevRecentHigh=%.2f elapsedResetAtHr=%.2f",
		// 	t.RecentHigh,
		// 	t.PreviousRecentHigh,
		// 	state.Sell.ElapsedBeforeResetHr,
		// )

		t.lastAddSell =
			state.Sell.NextLastAdd
	}
}

// --------------------------------------------------------------------------
// 5. Apply confidence-adjusted Pyramid transitions for the final selected side.
//
// BUY:
//   - update BUY win/latch state
//   - optionally rebase stale SELL latch
//
// SELL:
//   - update SELL win/latch state
//   - optionally rebase stale BUY latch
//
// --------------------------------------------------------------------------
func (t *Trader) applyPyramidDecisionTransitions(
	pyramid PyramidResult,
) {
	state := pyramid.State

	if state.Buy.UpdateWin {
		t.winLowBuy =
			state.Buy.NextWin
	}

	if state.Buy.UpdateLatched {
		t.latchedGateBuy =
			state.Buy.NextLatched

		if pyramid.Buy.HardLatchEligible {
			log.Printf(
				"[DEBUG] LATCH SET BUY: latchedGate=%.2f winLow=%.2f elapsedMin=%.1f tFloorMin=%.2f",
				pyramid.Buy.LatchBeforeClamp,
				t.winLowBuy,
				pyramid.Buy.ElapsedMin,
				pyramid.Buy.TFloorMin,
			)
		}

		if pyramid.Buy.LatchClampApplied {
			// log.Printf(
			// 	"[TRACE] pyramid.latch_clamp.buy old=%.8f last=%.8f new=%.8f",
			// 	pyramid.Buy.LatchBeforeClamp,
			// 	pyramid.Buy.LastAnchor,
			// 	pyramid.Buy.LatchAfterClamp,
			// )
		}
	}

	if state.Sell.UpdateWin {
		t.winHighSell =
			state.Sell.NextWin
	}

	if state.Sell.UpdateLatched {
		t.latchedGateSell =
			state.Sell.NextLatched

		if pyramid.Sell.HardLatchEligible {
			log.Printf(
				"[DEBUG] LATCH SET SELL: latchedGate=%.2f winHigh=%.2f elapsedMin=%.1f tFloorMin=%.2f",
				pyramid.Sell.LatchBeforeClamp,
				t.winHighSell,
				pyramid.Sell.ElapsedMin,
				pyramid.Sell.TFloorMin,
			)
		}

		if pyramid.Sell.LatchClampApplied {
			// log.Printf(
			// 	"[TRACE] pyramid.latch_clamp.sell old=%.8f last=%.8f new=%.8f",
			// 	pyramid.Sell.LatchBeforeClamp,
			// 	pyramid.Sell.LastAnchor,
			// 	pyramid.Sell.LatchAfterClamp,
			// )
		}
	}
}

func (t *Trader) applyPyramidRebaseTransactions(
	pyramid PyramidResult,
	signal Signal,
) {
	state := pyramid.State

	switch signal {
	case Buy:
		if !state.RebaseSellOnBuy {
			return
		}

		oldLatch := t.latchedGateSell
		oldWin := t.winHighSell

		t.latchedGateSell = state.NextSellLatch
		t.winHighSell = state.NextSellWin

		log.Printf(
			"[DEBUG] LATCH REBASE SELL: "+
				"ageHr=%.2f signal=%s "+
				"old_latched=%.2f old_winHigh=%.2f "+
				"new_latched=%.2f new_winHigh=%.2f price=%.2f",
			pyramid.Sell.ElapsedHr,
			signal,
			oldLatch,
			oldWin,
			t.latchedGateSell,
			t.winHighSell,
			pyramid.Buy.CurrentPrice,
		)

	case Sell:
		if !state.RebaseBuyOnSell {
			return
		}

		oldLatch := t.latchedGateBuy
		oldWin := t.winLowBuy

		t.latchedGateBuy = state.NextBuyLatch
		t.winLowBuy = state.NextBuyWin

		log.Printf(
			"[DEBUG] LATCH REBASE BUY: "+
				"ageHr=%.2f signal=%s "+
				"old_latched=%.2f old_winLow=%.2f "+
				"new_latched=%.2f new_winLow=%.2f price=%.2f",
			pyramid.Buy.ElapsedHr,
			signal,
			oldLatch,
			oldWin,
			t.latchedGateBuy,
			t.winLowBuy,
			pyramid.Sell.CurrentPrice,
		)
	}
}

// interpretPyramidRaw converts the confidence-independent Pyramid snapshot
// into the final confidence-adjusted Pyramid interpretation.
//
// Stage 2 (this function):
//   - Applies AI confidence to both BUY and SELL raw snapshots.
//   - Computes the final effective adverse percentage, timing,
//     gate prices, observation windows, win/latch progression,
//     and gate-pass status.
//   - Produces confidence-adjusted Pyramid state transitions.
//   - Does not decide BUY, SELL, or FLAT.
//
// The returned PyramidResult becomes one of the Case 5 raw materials,
// together with AI, MACD, and EMA, for the final entry decision engine.
func interpretPyramidRaw(
	raw PyramidRawResult,
	confidence float64,
) PyramidResult {
	started := time.Now()

	result := PyramidResult{
		Err:   raw.Err,
		State: raw.State,
	}

	defer func() {
		result.Elapsed = time.Since(started)
	}()

	if raw.Err != nil {
		return result
	}

	result.Buy, result.State.Buy =
		interpretPyramidSideRaw(
			raw.Buy,
			confidence,
			result.State.Buy,
		)

	result.Sell, result.State.Sell =
		interpretPyramidSideRaw(
			raw.Sell,
			confidence,
			result.State.Sell,
		)

	return result
}

func interpretPyramidSideRaw(
	raw PyramidSideRaw,
	confidence float64,
	transition PyramidSideTransition,
) (PyramidSideResult, PyramidSideTransition) {
	result := PyramidSideResult{
		Raw:          raw,
		Side:         raw.Side,
		CurrentPrice: raw.CurrentPrice,

		SpacingPass: raw.SpacingPass,

		Confidence: confidence,

		BasePct: raw.BasePct,

		ElapsedSec: raw.ElapsedSec,
		ElapsedMin: raw.ElapsedMin,
		ElapsedHr:  raw.ElapsedHr,

		LastAnchor: raw.LastAnchor,

		WinExtreme: raw.WinExtreme,
		Latched:    raw.Latched,

		LatchBufferPrice: raw.LatchBufferPrice,
	}

	// Preserve any raw transition values already proposed.
	nextWin := transition.NextWin
	if !transition.UpdateWin {
		nextWin = raw.WinExtreme
	}

	nextLatched := transition.NextLatched
	if !transition.UpdateLatched {
		nextLatched = raw.Latched
	}

	// Confidence scales both:
	//   1. adverse-price percentage;
	//   2. time before extreme observation/latching.
	gateMult := confidenceEffPctMultiplier(confidence)
	if gateMult <= 0 {
		gateMult = 1.0
	}
	result.GateMult = gateMult

	// Old behavior:
	// - Without time decay, effPct remains basePct.
	// - With time decay, decayed percentage is multiplied by gateMult.
	decayedPct := raw.BasePct
	effPct := raw.BasePct

	if raw.DecayLambda > 0 {
		decayedPct =
			raw.BasePct *
				math.Exp(-raw.DecayLambda*raw.ElapsedMin)

		if raw.DecayFloor > 0 &&
			decayedPct < raw.DecayFloor {

			decayedPct = raw.DecayFloor
		}

		effPct = decayedPct * gateMult
	}

	result.DecayedPct = decayedPct
	result.EffPct = effPct

	// Time required for the unscaled percentage to reach its floor.
	baseTFloorMin := 0.0

	if raw.DecayLambda > 0 &&
		raw.BasePct > raw.DecayFloor &&
		raw.DecayFloor > 0 {

		baseTFloorMin =
			math.Log(raw.BasePct/raw.DecayFloor) /
				raw.DecayLambda
	}

	tFloorMin := baseTFloorMin * gateMult

	result.BaseTFloorMin = baseTFloorMin
	result.TFloorMin = tFloorMin
	result.TFloorHr = tFloorMin / 60.0

	// No usable anchor means no Pyramid gate can pass.
	if raw.LastAnchor <= 0 || raw.CurrentPrice <= 0 {
		result.Reason = "missing_anchor_or_price"
		return result, transition
	}

	switch raw.Side {
	case SideBuy:
		result.BaselineGatePrice =
			raw.LastAnchor *
				(1.0 - effPct/100.0)

		result.EffectiveGatePrice =
			result.BaselineGatePrice

		// Phase 2 begins at tFloorMin:
		// observe lower prices while no hard latch exists.
		result.ObservingExtreme =
			raw.ElapsedMin >= tFloorMin &&
				nextLatched == 0

		if result.ObservingExtreme {
			if nextWin == 0 ||
				raw.CurrentPrice < nextWin {

				nextWin = raw.CurrentPrice

				transition.UpdateWin = true
				transition.NextWin = nextWin
			}

			// Soft gate before hard latch.
			result.SoftGateEligible =
				raw.ElapsedMin < 2.0*tFloorMin &&
					raw.RecentExtreme > 0

			if result.SoftGateEligible {
				result.SoftGatePrice =
					math.Max(
						result.BaselineGatePrice,
						raw.RecentExtreme,
					)

				result.EffectiveGatePrice =
					result.SoftGatePrice

				result.UsedSoftGate =
					result.SoftGatePrice !=
						result.BaselineGatePrice
			}
		} else if raw.ElapsedMin < tFloorMin {
			// Before observation begins, old winLow state is cleared.
			if nextWin != 0 {
				nextWin = 0

				transition.UpdateWin = true
				transition.NextWin = 0
			}
		}

		// Phase 3: latch the observed BUY low.
		result.HardLatchEligible =
			nextLatched == 0 &&
				raw.ElapsedMin >= 2.0*tFloorMin &&
				nextWin > 0

		if result.HardLatchEligible {
			nextLatched = nextWin

			transition.UpdateLatched = true
			transition.NextLatched = nextLatched
		}

		if nextLatched > 0 {
			result.LatchBeforeClamp = nextLatched

			finalLatch := nextLatched

			if raw.MarketRegime != RegimeUp {
				finalLatch =
					math.Min(
						raw.LastAnchor-raw.LatchBufferPrice,
						finalLatch,
					)
			}

			if finalLatch != nextLatched {
				result.LatchClampApplied = true

				nextLatched = finalLatch

				transition.UpdateLatched = true
				transition.NextLatched = nextLatched
			}

			result.LatchAfterClamp = nextLatched
			result.EffectiveGatePrice = nextLatched
			result.UsedLatchedGate = true
		}

		result.AdversePass =
			raw.CurrentPrice <=
				result.EffectiveGatePrice

	case SideSell:
		result.BaselineGatePrice =
			raw.LastAnchor *
				(1.0 + effPct/100.0)

		result.EffectiveGatePrice =
			result.BaselineGatePrice

		// Phase 2 begins at tFloorMin:
		// observe higher prices while no hard latch exists.
		result.ObservingExtreme =
			raw.ElapsedMin >= tFloorMin &&
				nextLatched == 0

		if result.ObservingExtreme {
			if nextWin == 0 ||
				raw.CurrentPrice > nextWin {

				nextWin = raw.CurrentPrice

				transition.UpdateWin = true
				transition.NextWin = nextWin
			}

			// Soft gate before hard latch.
			result.SoftGateEligible =
				raw.ElapsedMin < 2.0*tFloorMin &&
					raw.RecentExtreme > 0

			if result.SoftGateEligible {
				result.SoftGatePrice =
					math.Min(
						result.BaselineGatePrice,
						raw.RecentExtreme,
					)

				result.EffectiveGatePrice =
					result.SoftGatePrice

				result.UsedSoftGate =
					result.SoftGatePrice !=
						result.BaselineGatePrice
			}
		} else if raw.ElapsedMin < tFloorMin {
			// Before observation begins, old winHigh state is cleared.
			if nextWin != 0 {
				nextWin = 0

				transition.UpdateWin = true
				transition.NextWin = 0
			}
		}

		// Phase 3: latch the observed SELL high.
		result.HardLatchEligible =
			nextLatched == 0 &&
				raw.ElapsedMin >= 2.0*tFloorMin &&
				nextWin > 0

		if result.HardLatchEligible {
			nextLatched = nextWin

			transition.UpdateLatched = true
			transition.NextLatched = nextLatched
		}

		if nextLatched > 0 {
			result.LatchBeforeClamp = nextLatched

			finalLatch := nextLatched

			if raw.MarketRegime != RegimeDown {
				finalLatch =
					math.Max(
						raw.LastAnchor+raw.LatchBufferPrice,
						finalLatch,
					)
			}

			if finalLatch != nextLatched {
				result.LatchClampApplied = true

				nextLatched = finalLatch

				transition.UpdateLatched = true
				transition.NextLatched = nextLatched
			}

			result.LatchAfterClamp = nextLatched
			result.EffectiveGatePrice = nextLatched
			result.UsedLatchedGate = true
		}

		result.AdversePass =
			raw.CurrentPrice >=
				result.EffectiveGatePrice

	default:
		result.Reason = "invalid_side"
		return result, transition
	}

	result.GatePassed =
		result.SpacingPass &&
			result.AdversePass

	result.WinExtreme = nextWin
	result.Latched = nextLatched

	result.Reason = fmt.Sprintf(
		"side=%s|spacingPass=%t|gatePass=%t|price=%.8f|"+
			"anchor=%.8f|gatePrice=%.8f|latched=%.8f|"+
			"basePct=%.4f|decayedPct=%.4f|gateMult=%.4f|"+
			"effPct=%.4f|elapsedHr=%.2f|tFloorHr=%.2f|"+
			"soft=%t|hardLatch=%t|usedLatch=%t",
		result.Side,
		result.SpacingPass,
		result.GatePassed,
		result.CurrentPrice,
		result.LastAnchor,
		result.EffectiveGatePrice,
		result.Latched,
		result.BasePct,
		result.DecayedPct,
		result.GateMult,
		result.EffPct,
		result.ElapsedHr,
		result.TFloorHr,
		result.UsedSoftGate,
		result.HardLatchEligible,
		result.UsedLatchedGate,
	)

	return result, transition
}

// combineEntryRawMaterials is the final entry-producer selection engine.
//
// Producer priority:
//
//  1. Case11A — Peak-Reversal SELL
//  2. Case11B — Bottom-Reversal BUY
//  3. Case13A — Peak SELL
//  4. Case13B — Bottom BUY
//  5. Case14B — Uptrend buffered-latch BUY
//  6. Equity
//  7. NormalLegacy
//
// The first producer that emits BUY or SELL becomes the final decision.
//
// Ordinary producers receive the same immutable continuation snapshot. Their
// signal intelligence remains producer-owned; after a committed same-producer/
// same-side entry, the standardized continuation admission gate replaces the
// producer's native Pyramid/price admission for that continuation attempt and
// applies the producer-tier continuation ProfitGateMultiplier.
//
// Sizing, LongOnly, lot caps, pending-entry registration, funding approval,
// committed-reference mutation, and order placement remain outside this function.
func (t *Trader) combineEntryRawMaterials(
	ai AIResult,
	macd MACDResult,
	ema EMAPatternResult,
	pyramid PyramidResult,
	equity EquityResult,
	price float64,
	pendingCounts PendingProducerCounts,
	continuationRefs ProducerContinuationReferences,
) EntryDecision {
	// Continuation episode mutation is intentionally not performed here.
	// step.go owns the mirrored AI-direction reset and passes this function an
	// immutable producer+side reference snapshot for deterministic evaluation.

	regimeMult := t.RegimeMultiplier
	if regimeMult <= 0 {
		regimeMult = 1.0
	}

	d := EntryDecision{
		Signal:               Flat,
		Raw:                  ai.Raw,
		LegacySignal:         Flat,
		LogicOpinion:         Flat,
		Confidence:           ai.Confidence,
		Producer:             EntryProducerNone,
		PendingCancelPolicy:  PendingSignalCancelUnspecified,
		ProfitGateMultiplier: 0,

		PUp:           ai.PUp,
		BuyThreshold:  ai.BuyThreshold,
		SellThreshold: ai.SellThreshold,

		Pyramid: pyramid,
		Equity:  equity,

		LogicMACDLine:           macd.Line,
		LogicMACDLinePrev6:      macd.LinePrev6,
		LogicMACDTurn:           macd.Turn,
		LogicMACDHist:           macd.Hist,
		LogicMACDDHist:          macd.DHist,
		LogicMACDDSmooth:        macd.DSmooth,
		LogicMACDStrongPositive: macd.StrongPositive,
		LogicMACDStrongNegative: macd.StrongNegative,
		LogicMACDMomentumDown:   macd.MomentumDown,
		LogicMACDMomentumUp:     macd.MomentumUp,

		LogicEMASpread: ema.Spread,
		LogicEMA2050:   ema.EMA2050,

		LogicPatternHighPeak:    ema.HighPeak,
		LogicPatternLowBottom:   ema.LowBottom,
		LogicPatternPriceDownUp: ema.PriceDownUp,
		LogicPatternPriceUpDown: ema.PriceUpDown,
		LogicPatternBuy:         ema.PatternBuy,
		LogicPatternSell:        ema.PatternSell,

		LogicEPS:       macd.EPS,
		LogicRegimeEPS: macd.RegimeEPS,
		LogicBaseEPS:   macd.BaseEPS,
		MarketRegime:   t.MarketRegime,
		RegimeMult:     regimeMult,
	}

	// Preserve evaluator-level diagnostics regardless of producer outcome.
	d.PyramidBuySpacingPass =
		pyramid.Buy.SpacingPass
	d.PyramidBuyAdversePass =
		pyramid.Buy.AdversePass
	d.PyramidBuyGatePassed =
		pyramid.Buy.GatePassed

	d.PyramidSellSpacingPass =
		pyramid.Sell.SpacingPass
	d.PyramidSellAdversePass =
		pyramid.Sell.AdversePass
	d.PyramidSellGatePassed =
		pyramid.Sell.GatePassed

	d.EquityBuyTrigger =
		equity.BuyTrigger
	d.EquitySellTrigger =
		equity.SellTrigger

	// -------------------------------------------------------------
	// Case 11 — Independent reversal producers.
	// -------------------------------------------------------------
	if applyCase11ReversalProducer(
		&d,
		ai,
		macd,
		ema,
		pyramid,
		price,
		continuationRefs,
	) {
		return d
	}

	// -------------------------------------------------------------
	// Case 13 — Independent persistent-trend reversal producers.
	// -------------------------------------------------------------
	if applyCase13Producer(
		&d,
		ai,
		macd,
		ema,
		pyramid,
		price,
		t.RecentLow,
		t.RecentHigh,
		t.MarketRegime,
		pendingCounts,
		continuationRefs,
	) {
		return d
	}

	// -------------------------------------------------------------
	// Case14B — Buffered-latch BUY in an UP regime.
	// price <= latch: NormalLegacy territory
	// latch < price <= buffered latch: Case14B territory
	// price > buffered latch: no Case14B entry
	// pending Case14B > 0: Case14B disabled
	// -------------------------------------------------------------
	if applyCase14BUptrendBuyProducer(
		&d,
		ai,
		macd,
		ema,
		pyramid,
		price,
		t.MarketRegime,
		pendingCounts,
		continuationRefs,
	) {
		return d
	}

	// -------------------------------------------------------------
	// Equity — independent Equity threshold + funding producer.
	// AI / Logic / Pyramid are diagnostics only and do not gate Equity.
	// -------------------------------------------------------------
	if applyEquityProducer(
		&d,
		ai,
		macd,
		ema,
		pyramid,
		equity,
		pendingCounts,
		continuationRefs,
	) {
		return d
	}

	// -------------------------------------------------------------
	// NormalLegacy — AI + Logic direction.
	//
	// First entry uses the native matching complete Pyramid gate.
	// After a committed same-producer/same-side fill, continuation keeps
	// the same signal qualification but replaces Pyramid admission with
	// the standardized committed-reference +/-0.20% gate.
	// -------------------------------------------------------------
	if applyNormalLegacyProducer(
		&d,
		ai,
		macd,
		ema,
		pyramid,
		price,
		continuationRefs,
	) {
		return d
	}

	return d
}

// SignalToSide converts the intent into a broker side.
func (d EntryDecision) SignalToSide() (OrderSide, bool) {
	switch d.Signal {
	case Buy:
		return SideBuy, true
	case Sell:
		return SideSell, true
	default:
		return "", false
	}
}

const trendMult = 0.80

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

func shouldExitByAILogic(lot *Position, d EntryDecision) bool {
	if lot.Side == SideBuy {
		return d.Signal == Sell
	}
	if lot.Side == SideSell {
		return d.Signal == Buy
	}
	return false
}

// lowestLow returns the lowest candle low within the rolling lookback window
// together with the timestamp of the candle supplying that low.
// If no candle falls inside the window, returns zero values.
//
// Example:
//
//	low, lowAt := lowestLow(execHistory, 4*time.Hour)
func lowestLow(candles []Candle, lookback time.Duration) (float64, time.Time) {
	if len(candles) == 0 || lookback <= 0 {
		return 0, time.Time{}
	}

	latest := candles[len(candles)-1].Time
	if latest.IsZero() {
		latest = time.Now().UTC()
	}

	cutoff := latest.Add(-lookback)

	lowest := 0.0
	lowestAt := time.Time{}
	found := false

	for i := len(candles) - 1; i >= 0; i-- {
		c := candles[i]

		// stop once outside window
		if !c.Time.IsZero() && c.Time.Before(cutoff) {
			break
		}

		if !found || c.Low < lowest {
			lowest = c.Low
			lowestAt = c.Time
			found = true
		}
	}

	if !found {
		return 0, time.Time{}
	}

	return lowest, lowestAt
}

// highestHigh returns the highest candle high within the rolling lookback window
// together with the timestamp of the candle supplying that high.
// If no candle falls inside the window, returns zero values.
//
// Example:
//
//	high, highAt := highestHigh(execHistory, 4*time.Hour)
func highestHigh(candles []Candle, lookback time.Duration) (float64, time.Time) {
	if len(candles) == 0 || lookback <= 0 {
		return 0, time.Time{}
	}

	latest := candles[len(candles)-1].Time
	if latest.IsZero() {
		latest = time.Now().UTC()
	}

	cutoff := latest.Add(-lookback)

	highest := 0.0
	highestAt := time.Time{}
	found := false

	for i := len(candles) - 1; i >= 0; i-- {
		c := candles[i]

		// stop once outside window
		if !c.Time.IsZero() && c.Time.Before(cutoff) {
			break
		}

		if !found || c.High > highest {
			highest = c.High
			highestAt = c.Time
			found = true
		}
	}

	if !found {
		return 0, time.Time{}
	}

	return highest, highestAt
}

func (t *Trader) updateMarketRegimeFromRecentExtremes(candles []Candle, wallNow time.Time) {
	t.mu.Lock()
	defer t.mu.Unlock()
	const (
		startMult = 2.0
		stepMult  = 0.25
		maxMult   = 3.0
	)

	t.RecentHigh, t.RecentHighAt =
		highestHigh(candles, 12*time.Hour)
	t.RecentLow, t.RecentLowAt =
		lowestLow(candles, 12*time.Hour)

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

	shortenRegimeHalf := func(reason string) {
		if t.RegimeUntil.IsZero() {
			return
		}

		remaining := t.RegimeUntil.Sub(wallNow)
		if remaining <= 0 {
			return
		}

		newRemaining := remaining / 2
		t.RegimeUntil = wallNow.Add(newRemaining)

		log.Printf(
			"[TRACE] regime.shorten_half "+
				"regime=%s reason=%s remaining_before=%s remaining_after=%s until=%s",
			t.MarketRegime,
			reason,
			remaining,
			newRemaining,
			t.RegimeUntil.Format(time.RFC3339),
		)
	}

	if freshLow {
		t.FreshLowAt = wallNow
	}
	if freshHigh {
		t.FreshHighAt = wallNow
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
		if expiredByTime {
			toNormal(
				"regime_down_time_expired",
			)
			changed = true

		} else if freshLow {
			extendRegime(
				RegimeDown,
				"fresh_12h_low_extend_down",
			)
			changed = true

		} else if freshHigh {
			shortenRegimeHalf(
				"fresh_12h_high_against_down",
			)
			changed = true
		}

	case RegimeUp:
		if expiredByTime {
			toNormal(
				"regime_up_time_expired",
			)
			changed = true

		} else if freshHigh {
			extendRegime(
				RegimeUp,
				"fresh_12h_high_extend_up",
			)
			changed = true

		} else if freshLow {
			shortenRegimeHalf(
				"fresh_12h_low_against_up",
			)
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
	if !t.FreshLowAt.IsZero() {
		lowAgeHr = wallNow.Sub(t.FreshLowAt).Hours()
	}
	if !t.FreshHighAt.IsZero() {
		highAgeHr = wallNow.Sub(t.FreshHighAt).Hours()
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

// applyRecoveryDebtFromExit maintains the bot-wide signed cumulative
// realized PnL balance using the existing RecoveryDebtUSD sign convention:
//
//	RecoveryDebtUSD > 0  => cumulative realized net loss
//	RecoveryDebtUSD == 0 => break-even
//	RecoveryDebtUSD < 0  => cumulative realized net profit
//
// pnl is authoritative realized NET PnL:
//
//	pnl < 0 => loss, so RecoveryDebtUSD increases
//	pnl > 0 => profit, so RecoveryDebtUSD decreases
//
// Do NOT clamp at zero. A negative RecoveryDebtUSD is meaningful and
// represents realized profit exceeding realized losses.
func (t *Trader) applyRecoveryDebtFromExit(pnl float64) {
	if pnl == 0 ||
		math.IsNaN(pnl) ||
		math.IsInf(pnl, 0) {
		return
	}

	t.RecoveryDebtUSD -= pnl
}

func (t *Trader) toNormal(reason string) {

	if t.MarketRegime == RegimeNormal {
		return
	}

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

func (t *Trader) shouldResetRegime(side OrderSide) bool {

	switch {

	case t.MarketRegime == RegimeUp &&
		side == SideBuy:

		return true

	case t.MarketRegime == RegimeDown &&
		side == SideSell:

		return true
	}

	return false
}

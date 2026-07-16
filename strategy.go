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

// EntryDecision contains the final entry-side decision and all evidence used
// to produce it.
//
// Exit-specific information belongs exclusively to ExitDecision.
type EntryDecision struct {
	Signal       Signal
	Raw          Signal
	LegacySignal Signal
	Confidence   float64

	DecisionSource EntryDecisionSource

	// Model / AI summary.
	PUp           float64
	BuyThreshold  float64
	SellThreshold float64

	// Combined legacy MACD + EMA opinion.
	LogicOpinion Signal

	// Complete Case 5 interpreted evidence.
	Pyramid PyramidResult
	Equity  EquityResult

	// Selected-side summary.
	PyramidPass   bool
	PyramidReason string
	EquityPass    bool
	EquityReason  string

	// Logic MACD raw materials.
	LogicMACDLine           float64
	LogicMACDTurn           float64
	LogicMACDHist           float64
	LogicMACDDHist          float64
	LogicMACDDSmooth        float64
	LogicMACDStrongPositive bool
	LogicMACDStrongNegative bool
	LogicMACDMomentumDown   bool
	LogicMACDMomentumUp     bool

	// Logic EMA raw materials.
	LogicEMASpread float64
	LogicEMA2050   float64

	// Logic pattern raw materials.
	LogicPatternHighPeak    bool
	LogicPatternLowBottom   bool
	LogicPatternPriceDownUp bool
	LogicPatternPriceUpDown bool
	LogicPatternBuy         bool
	LogicPatternSell        bool

	// Logic interpretation context.
	LogicEPS       float64
	LogicBaseEPS   float64
	LogicRegimeEPS float64
	MarketRegime   MarketRegime
	RegimeMult     float64
}

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

type EntryDecisionSource string

const (
	EntryDecisionSourceNone          EntryDecisionSource = ""
	EntryDecisionSourceLegacyPyramid EntryDecisionSource = "LEGACY_PYRAMID"
	EntryDecisionSourceLegacyEquity  EntryDecisionSource = "LEGACY_EQUITY"
)

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

// EquityResult preserves the raw Equity snapshot, applies the legacy signal,
// applies available spare funding, and proposes the Equity BUY/SELL trigger.
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
		t.cfg.AIFeatureDim,
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
	Line    float64
	Turn    float64
	Hist    float64
	DHist   float64
	DSmooth float64

	// Raw momentum inputs produced by the snapshot.
	MomentumDown bool
	MomentumUp   bool

	Err     error
	Elapsed time.Duration
}

type MACDResult struct {
	Opinion Signal
	EPS     float64

	Line    float64
	Turn    float64
	Hist    float64
	DHist   float64
	DSmooth float64

	StrongPositive bool
	StrongNegative bool
	MomentumDown   bool
	MomentumUp     bool

	Err     error
	Elapsed time.Duration
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

// interpretEquityRaw applies the legacy direction and available spare funding
// to the direction-independent Equity snapshot.
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
) float64 {

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

	return eps
}

func interpretMACD(
	raw MACDSnapshotResult,
	eps float64,
) MACDResult {

	result := MACDResult{
		EPS:          eps,
		Line:         raw.Line,
		Turn:         raw.Turn,
		Hist:         raw.Hist,
		DHist:        raw.DHist,
		DSmooth:      raw.DSmooth,
		MomentumDown: raw.MomentumDown,
		MomentumUp:   raw.MomentumUp,
		Err:          raw.Err,
		Elapsed:      raw.Elapsed,
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
	WinExtreme float64
	Latched    float64

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
		log.Printf("[TRACE] pyramid.latch_extend side=BUY elapsedResetAtHr=%.2f", state.Buy.ElapsedBeforeResetHr)
		t.lastAddBuy =
			state.Buy.NextLastAdd
	}

	if state.Sell.UpdateLastAdd {
		log.Printf("[TRACE] pyramid.latch_extend side=SELL elapsedResetAtHr=%.2f", state.Sell.ElapsedBeforeResetHr)
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
	state PyramidStateTransitions,
	finalSignal Signal,
) {
	switch finalSignal {
	case Buy:
		if state.Buy.UpdateWin {
			t.winLowBuy =
				state.Buy.NextWin
		}

		if state.Buy.UpdateLatched {
			t.latchedGateBuy =
				state.Buy.NextLatched
		}

		if state.RebaseSellOnBuy {
			t.latchedGateSell =
				state.NextSellLatch

			t.winHighSell =
				state.NextSellWin
		}

	case Sell:
		if state.Sell.UpdateWin {
			t.winHighSell =
				state.Sell.NextWin
		}

		if state.Sell.UpdateLatched {
			t.latchedGateSell =
				state.Sell.NextLatched
		}

		if state.RebaseBuyOnSell {
			t.latchedGateBuy =
				state.NextBuyLatch

			t.winLowBuy =
				state.NextBuyWin
		}
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
			finalLatch := nextLatched

			// Preserve the original regime-sensitive clamp.
			if raw.MarketRegime != RegimeUp {
				finalLatch =
					math.Min(
						raw.LastAnchor-raw.LatchBufferPrice,
						finalLatch,
					)
			}

			if finalLatch != nextLatched {
				nextLatched = finalLatch

				transition.UpdateLatched = true
				transition.NextLatched = nextLatched
			}

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
			finalLatch := nextLatched

			// Preserve the original regime-sensitive clamp.
			if raw.MarketRegime != RegimeDown {
				finalLatch =
					math.Max(
						raw.LastAnchor+raw.LatchBufferPrice,
						finalLatch,
					)
			}

			if finalLatch != nextLatched {
				nextLatched = finalLatch

				transition.UpdateLatched = true
				transition.NextLatched = nextLatched
			}

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

// combineEntryRawMaterials is the Case 5 final entry-decision engine.
//
// Initial policy:
//
//  1. Legacy AI + MACD + EMA remains the only directional producer.
//  2. A matching executable Equity trigger bypasses Pyramid.
//  3. Otherwise, the matching Pyramid gate must pass.
//  4. Pyramid-only and Equity-only directions are not enabled yet.
//  5. Sizing, LongOnly, lot caps, pending checks, and placement remain outside.
func (t *Trader) combineEntryRawMaterials(
	ai AIResult,
	macd MACDResult,
	ema EMAPatternResult,
	pyramid PyramidResult,
	equity EquityResult,
	legacySignal Signal,
	logicOpinion Signal,
) EntryDecision {
	regimeMult := t.RegimeMultiplier
	if regimeMult <= 0 {
		regimeMult = 1.0
	}

	d := EntryDecision{
		Signal:         Flat,
		Raw:            ai.Raw,
		LegacySignal:   legacySignal,
		Confidence:     ai.Confidence,
		DecisionSource: EntryDecisionSourceNone,

		PUp:           ai.PUp,
		BuyThreshold:  ai.BuyThreshold,
		SellThreshold: ai.SellThreshold,

		LogicOpinion: logicOpinion,

		Pyramid: pyramid,
		Equity:  equity,

		LogicMACDLine:           macd.Line,
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

		LogicEPS:     macd.EPS,
		MarketRegime: t.MarketRegime,
		RegimeMult:   regimeMult,
	}

	switch legacySignal {
	case Buy:
		d.PyramidPass = pyramid.Buy.GatePassed
		d.PyramidReason = pyramid.Buy.Reason
		d.EquityPass = equity.BuyTrigger
		d.EquityReason = equity.Reason
		switch {
		case equity.BuyTrigger:
			d.Signal = Buy
			d.DecisionSource = EntryDecisionSourceLegacyEquity

		case pyramid.Buy.GatePassed:
			d.Signal = Buy
			d.DecisionSource = EntryDecisionSourceLegacyPyramid
		}

	case Sell:
		d.PyramidPass = pyramid.Sell.GatePassed
		d.PyramidReason = pyramid.Sell.Reason
		d.EquityPass = equity.SellTrigger
		d.EquityReason = equity.Reason

		switch {
		case equity.SellTrigger:
			d.Signal = Sell
			d.DecisionSource = EntryDecisionSourceLegacyEquity

		case pyramid.Sell.GatePassed:
			d.Signal = Sell
			d.DecisionSource = EntryDecisionSourceLegacyPyramid
		}

	default:
		d.Signal = Flat
		d.Confidence = 0
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

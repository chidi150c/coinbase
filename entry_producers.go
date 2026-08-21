// ---------------------------------------------------------------------------------------------
// FILE: entry_producers.go — Independent entry-direction producers
//
// Overview
//
//	entry_producers.go contains independent BUY and SELL producers that recognize specific
//	market phenomena from already-computed evaluator results. Each producer may emit an
//	independent directional decision without relying on the legacy AI/Logic decision path.
//
//	A producer represents one well-defined market phenomenon (for example, Peak Reversal,
//	Bottom Reversal, Capitulation Bottom, Momentum Exhaustion, etc.) and is evaluated in a
//	deterministic priority order by combineEntryRawMaterials().
//
// Responsibilities
//
//   - Consume immutable evaluator outputs and market-state snapshots.
//   - Detect a specific market phenomenon.
//   - Produce BUY or SELL independently when its conditions are satisfied.
//   - Populate EntryDecision diagnostics for both successful and unsuccessful evaluations.
//   - Assign a unique EntryProducer for traceability.
//   - Emit sufficient TRACE logging for why-trade forensic analysis.
//
// Non-Responsibilities
//
//   - AI inference or feature computation.
//   - Indicator calculations (MACD, EMA, Pyramid, Equity, etc.).
//   - Position sizing.
//   - Funding or balance validation.
//   - LongOnly restrictions.
//   - Pending-entry management.
//   - Lot limits.
//   - Order submission.
//   - Trader state mutation.
//
// Design Principles
//
//   - Each producer models exactly one market phenomenon.
//   - Producers remain independent of one another.
//   - Use the minimum sufficient combination of raw materials.
//   - Producers may optionally separate into:
//   - Arm (persistent setup)
//   - Trigger (final confirmation)
//     when that structure naturally represents the phenomenon.
//   - Every producer should be independently testable.
//
// Priority
//
//	Producers are evaluated in deterministic priority order by the entry
//	decision engine. The first producer that emits a directional signal
//	becomes the final entry decision. If no independent producer fires,
//	control falls back to the legacy Case 5 producer.
//
// Evolution
//
//	New producers should be created through the Forensic Producer Synthesis
//	(FPS) methodology. Each producer should:
//
//	1. Represent one observable market phenomenon.
//	2. Have a clearly documented objective.
//	3. Use only the minimum required raw materials.
//	4. Define its own diagnostics and Producer.
//	5. Be historically validated before production use.
//
// ---------------------------------------------------------------------------------------------
package main

import (
	"fmt"
	"log"
	"strings"
	"time"
)

type EntryProducer string

const (
	EntryProducerNone EntryProducer = ""

	EntryProducerNormalLegacy EntryProducer = "NormalLegacy"

	EntryProducerEquity EntryProducer = "Equity"

	EntryProducerCase3AReplacement EntryProducer = "Case3AReplacement"

	EntryProducerCase11APeakReversal EntryProducer = "Case11APeakReversal"

	EntryProducerCase11BBottomReversal EntryProducer = "Case11BBottomReversal"

	EntryProducerCase13APeakSell EntryProducer = "Case13APeakSell"

	EntryProducerCase13BBottomBuy EntryProducer = "Case13BBottomBuy"

	EntryProducerCase14BUptrendBuy EntryProducer = "Case14BUptrendBuy"
)

type PendingSignalCancelPolicy string

const (
	PendingSignalCancelUnspecified PendingSignalCancelPolicy = ""

	// Cancel when the current decision becomes FLAT or opposite.
	PendingSignalCancelOnFlatOrOpposite PendingSignalCancelPolicy = "CancelOnFlatOrOpposite"

	// Ignore FLAT; cancel only when the current decision becomes opposite.
	PendingSignalCancelOnOpposite PendingSignalCancelPolicy = "CancelOnOpposite"

	// Do not cancel based on the ordinary entry decision signal.
	PendingSignalCancelDisabled PendingSignalCancelPolicy = "Disabled"
)

// EntryDecision contains the final entry-side decision and all evidence used
// to produce it.
//
// Exit-specific information belongs exclusively to ExitDecision.
type EntryDecision struct {
	// Final decision.
	Signal       Signal
	Raw          Signal
	LegacySignal Signal
	LogicOpinion Signal
	Producer     EntryProducer
	Confidence   float64

	// AI / model context.
	PUp           float64
	BuyThreshold  float64
	SellThreshold float64

	// Market / interpretation context.
	MarketRegime   MarketRegime
	RegimeMult     float64
	LogicEPS       float64
	LogicBaseEPS   float64
	LogicRegimeEPS float64

	// Complete Case 5 evaluator outputs.
	Pyramid PyramidResult
	Equity  EquityResult

	// Selected-side summary.
	PyramidPass   bool
	PyramidReason string
	EquityPass    bool
	EquityReason  string

	// MACD evidence.
	LogicMACDLine           float64
	LogicMACDLinePrev6      float64
	LogicMACDTurn           float64
	LogicMACDHist           float64
	LogicMACDDHist          float64
	LogicMACDDSmooth        float64
	LogicMACDStrongPositive bool
	LogicMACDStrongNegative bool
	LogicMACDMomentumDown   bool
	LogicMACDMomentumUp     bool

	// EMA evidence.
	LogicEMASpread float64
	LogicEMA2050   float64

	// Pattern evidence.
	LogicPatternHighPeak    bool
	LogicPatternLowBottom   bool
	LogicPatternPriceDownUp bool
	LogicPatternPriceUpDown bool
	LogicPatternBuy         bool
	LogicPatternSell        bool

	// Peak-reversal (Case 11A).
	MACDPrePeakZone  bool
	PeakReversalSell bool
	// Bottom-reversal (Case 11B).
	MACDPreBottomZone bool
	BottomReversalBuy bool

	// Pyramid evaluation.
	PyramidBuySpacingPass  bool
	PyramidBuyAdversePass  bool
	PyramidBuyGatePassed   bool
	PyramidSellSpacingPass bool
	PyramidSellAdversePass bool
	PyramidSellGatePassed  bool

	// Equity evaluation.
	EquityBuyTrigger  bool
	EquitySellTrigger bool
	// Case 13 — Capitulation-Bottom BUY evidence.
	NearRecentLowPct     float64
	PriceNearRecentLow   bool
	NearRecentHighPct    float64
	PriceNearRecentHigh  bool
	ProfitGateMultiplier float64
	ProducerReason       string
	PendingCancelPolicy  PendingSignalCancelPolicy
	AssignRunner         bool
}

type EntryPolicy struct {
	ResetLastAdd     bool
	ResetWinExtreme  bool
	ResetLatchedGate bool
	ResetRegime      bool
}

func entryPolicyForSource(source EntryProducer) EntryPolicy {
	switch source {

	case EntryProducerNormalLegacy:
		return EntryPolicy{
			ResetLastAdd:     true,
			ResetWinExtreme:  true,
			ResetLatchedGate: true,
			ResetRegime:      true,
		}

	case EntryProducerEquity:
		return EntryPolicy{
			ResetLastAdd:     true,
			ResetWinExtreme:  true,
			ResetLatchedGate: true,
			ResetRegime:      true,
		}

	case EntryProducerCase3AReplacement:
		return EntryPolicy{
			ResetLastAdd:     true,
			ResetWinExtreme:  true,
			ResetLatchedGate: true,
			ResetRegime:      false,
		}
	case EntryProducerCase11APeakReversal,
		EntryProducerCase11BBottomReversal:
		return EntryPolicy{
			ResetLastAdd:     true,
			ResetWinExtreme:  true,
			ResetLatchedGate: true,
			ResetRegime:      false,
		}

	case EntryProducerCase13APeakSell,
		EntryProducerCase13BBottomBuy:
		return EntryPolicy{
			ResetLastAdd:     true,
			ResetWinExtreme:  true,
			ResetLatchedGate: true,
			ResetRegime:      false,
		}

	case EntryProducerCase14BUptrendBuy:
		return EntryPolicy{
			ResetLastAdd:     true,
			ResetWinExtreme:  true,
			ResetLatchedGate: true,
			ResetRegime:      false,
		}

	default:
		panic(fmt.Sprintf("entryPolicyForSource: unsupported source %q", source))
	}
}

// applyNormalLegacyProducer evaluates the standard AI + Logic + Pyramid
// entry producer.
//
// It produces only when:
//
//   - AI and Logic resolve to BUY or SELL; and
//   - the matching complete Pyramid gate passes.
func applyNormalLegacyProducer(
	d *EntryDecision,
	ai AIResult,
	macd MACDResult,
	ema EMAPatternResult,
	pyramid PyramidResult,
) bool {
	if d == nil {
		return false
	}

	legacy :=
		evaluateLegacyDirection(
			ai,
			macd,
			ema,
		)

	switch legacy.Signal {
	case Buy:
		produced :=
			pyramid.Buy.GatePassed

		// log.Printf(
		// 	"[TRACE] normal_legacy.buy.evaluate "+
		// 		"ai_raw=%s logic=%s legacy=%s "+
		// 		"strong_negative=%t momentum_up=%t pattern_buy=%t "+
		// 		"spacing=%t adverse=%t gate=%t produced=%t",
		// 	ai.Raw,
		// 	legacy.LogicOpinion,
		// 	legacy.Signal,
		// 	macd.StrongNegative,
		// 	macd.MomentumUp,
		// 	ema.PatternBuy,
		// 	pyramid.Buy.SpacingPass,
		// 	pyramid.Buy.AdversePass,
		// 	pyramid.Buy.GatePassed,
		// 	produced,
		// )

		if !produced {
			return false
		}

		d.Signal = Buy
		d.LogicOpinion = legacy.LogicOpinion
		d.LegacySignal = legacy.Signal
		d.PyramidPass = pyramid.Buy.GatePassed
		d.PyramidReason = pyramid.Buy.Reason
		d.Producer = EntryProducerNormalLegacy
		d.PendingCancelPolicy = PendingSignalCancelOnFlatOrOpposite
		d.ProducerReason = fmt.Sprintf(
			"normal_legacy_buy|"+
				"ai_raw=%s|logic=%s|"+
				"strong_negative=%t|momentum_up=%t|pattern_buy=%t|"+
				"spacing=%t|adverse=%t|gate=%t|"+
				"latched=%.8f|gate_price=%.8f",
			ai.Raw,
			legacy.LogicOpinion,
			macd.StrongNegative,
			macd.MomentumUp,
			ema.PatternBuy,
			pyramid.Buy.SpacingPass,
			pyramid.Buy.AdversePass,
			pyramid.Buy.GatePassed,
			pyramid.Buy.Latched,
			pyramid.Buy.EffectiveGatePrice,
		)

		return true

	case Sell:
		produced :=
			pyramid.Sell.GatePassed

		// log.Printf(
		// 	"[TRACE] normal_legacy.sell.evaluate "+
		// 		"ai_raw=%s logic=%s legacy=%s "+
		// 		"strong_positive=%t momentum_down=%t pattern_sell=%t "+
		// 		"spacing=%t adverse=%t gate=%t produced=%t",
		// 	ai.Raw,
		// 	legacy.LogicOpinion,
		// 	legacy.Signal,
		// 	macd.StrongPositive,
		// 	macd.MomentumDown,
		// 	ema.PatternSell,
		// 	pyramid.Sell.SpacingPass,
		// 	pyramid.Sell.AdversePass,
		// 	pyramid.Sell.GatePassed,
		// 	produced,
		// )

		if !produced {
			return false
		}

		d.Signal = Sell
		d.LogicOpinion = legacy.LogicOpinion
		d.LegacySignal = legacy.Signal

		d.PyramidPass =
			pyramid.Sell.GatePassed
		d.PyramidReason =
			pyramid.Sell.Reason
		d.Producer = EntryProducerNormalLegacy
		d.PendingCancelPolicy = PendingSignalCancelOnFlatOrOpposite
		d.ProducerReason = fmt.Sprintf(
			"normal_legacy_sell|"+
				"ai_raw=%s|logic=%s|"+
				"strong_positive=%t|momentum_down=%t|pattern_sell=%t|"+
				"spacing=%t|adverse=%t|gate=%t|"+
				"latched=%.8f|gate_price=%.8f",
			ai.Raw,
			legacy.LogicOpinion,
			macd.StrongPositive,
			macd.MomentumDown,
			ema.PatternSell,
			pyramid.Sell.SpacingPass,
			pyramid.Sell.AdversePass,
			pyramid.Sell.GatePassed,
			pyramid.Sell.Latched,
			pyramid.Sell.EffectiveGatePrice,
		)

		return true

	default:
		// log.Printf(
		// 	"[TRACE] normal_legacy.evaluate "+
		// 		"ai_raw=%s logic=%s legacy=%s "+
		// 		"normal_buy=%t normal_sell=%t produced=false",
		// 	ai.Raw,
		// 	legacy.LogicOpinion,
		// 	legacy.Signal,
		// 	legacy.NormalBuy,
		// 	legacy.NormalSell,
		// )

		return false
	}
}

// newProducerDecisionLifecycle creates the lifecycle state for a producer
// decision that has been accepted by the entry decision engine.
//
// It converts the transient EntryDecision into:
//   - PendingIntent, which carries the producer's execution intent and policy
//     through the pending/order lifecycle; and
//   - ProducerAttempt, which provides the observability record used to track
//     that decision through subsequent producer stages.
//
// Producer-owned semantics established by the decision, including Producer,
// PendingCancelPolicy, ProducerReason, and AssignRunner, are copied into the
// PendingIntent here so they survive beyond EntryDecision processing.
func newProducerDecisionLifecycle(d *EntryDecision) (*PendingIntent, *ProducerAttempt) {
	if d == nil || d.Producer == EntryProducerNone {
		return nil, nil
	}
	createdAt := time.Now().UTC()
	var side OrderSide
	if resolvedSide, ok := d.SignalToSide(); ok {
		side = resolvedSide
	}
	intent := &PendingIntent{
		CreatedAt: createdAt,
		DecisionID: FormatDecisionID(
			d.Producer,
			createdAt,
		),
		Producer:            d.Producer,
		PendingCancelPolicy: d.PendingCancelPolicy,
		ProducerReason:      d.ProducerReason,
		AssignRunner:        d.AssignRunner,
		Side:                side,
	}
	attemptSide := fmt.Sprint(d.Signal)
	if side == SideBuy || side == SideSell {
		attemptSide = fmt.Sprint(side)
	}
	attempt := &ProducerAttempt{
		DecisionID: intent.DecisionID,
		CreatedAt:  intent.CreatedAt,
		Producer:   intent.Producer,
		Side:       attemptSide,
		Events: make(
			map[ProducerStage]ProducerEvent,
		),
	}
	return intent, attempt
}

func (t *Trader) evaluateEquityProducerMaterial(
	ai AIResult,
	macd MACDResult,
	ema EMAPatternResult,
	balanceSnapshotMaxAge time.Duration,
	reservedShortQuoteWithFee float64,
	reservedLongBase float64,
) (EquityResult, error) {
	equityRaw := t.evaluateEquityRaw()
	legacy :=
		evaluateLegacyDirection(
			ai,
			macd,
			ema,
		)
	var (
		symQ       string
		availQuote float64
		quoteStep  float64

		symB      string
		availBase float64
		baseStep  float64

		spareQuote float64
		spareBase  float64
	)

	if legacy.Signal == Buy ||
		legacy.Signal == Sell {

		equityBalance, ok :=
			t.getBalanceSpare(
				balanceSnapshotMaxAge,
				reservedShortQuoteWithFee,
				reservedLongBase,
			)

		if !ok {
			ageMS := int64(-1)

			if !equityBalance.Snapshot.UpdatedAt.IsZero() {
				ageMS =
					time.Since(
						equityBalance.Snapshot.UpdatedAt,
					).Milliseconds()
			}

			return EquityResult{}, fmt.Errorf(
				"equity balance cache unavailable: "+
					"legacy=%s age_ms=%d",
				legacy.Signal,
				ageMS,
			)
		}

		availQuote = equityBalance.AvailQuote
		quoteStep = equityBalance.QuoteStep
		availBase = equityBalance.AvailBase
		baseStep = equityBalance.BaseStep

		spareQuote = equityBalance.SpareQuote
		spareBase = equityBalance.SpareBase

		symQ = equityBalance.Snapshot.SymQuote
		symB = equityBalance.Snapshot.SymBase

		log.Printf(
			"[TRACE] equity.balance_cache.hit "+
				"legacy=%s age_ms=%d "+
				"quote=%.8f base=%.8f",
			legacy.Signal,
			time.Since(
				equityBalance.Snapshot.UpdatedAt,
			).Milliseconds(),
			availQuote,
			availBase,
		)

		switch legacy.Signal {
		case Buy:
			if strings.TrimSpace(symQ) == "" ||
				quoteStep <= 0 {

				return EquityResult{}, fmt.Errorf(
					"invalid cached quote metadata: "+
						"symbol=%q step=%.8f",
					symQ,
					quoteStep,
				)
			}

		case Sell:
			if strings.TrimSpace(symB) == "" ||
				baseStep <= 0 {

				return EquityResult{}, fmt.Errorf(
					"invalid cached base metadata: "+
						"symbol=%q step=%.8f",
					symB,
					baseStep,
				)
			}
		}
	}

	equityResult :=
		interpretEquityRaw(
			equityRaw,
			legacy.Signal,
			spareQuote,
			spareBase,
			quoteStep,
			baseStep,
		)

	if equityResult.Err != nil {
		return EquityResult{},
			fmt.Errorf(
				"interpret equity raw: %w",
				equityResult.Err,
			)
	}

	return equityResult, nil
}

// applyEquityProducer evaluates the AI + Logic + Pyramid + Equity producer.
//
// Equity is independently attributed, but it still requires:
//
//   - a resolved legacy BUY or SELL direction;
//   - the matching complete Pyramid gate; and
//   - the matching Equity trigger.
//   - the matching Equity trigger.
//
// The Pyramid result remains attached as diagnostics, but it does not gate
// an Equity-triggered entry.
func applyEquityProducer(
	d *EntryDecision,
	ai AIResult,
	macd MACDResult,
	ema EMAPatternResult,
	pyramid PyramidResult,
	equity EquityResult,
) bool {
	if d == nil {
		return false
	}

	legacy :=
		evaluateLegacyDirection(
			ai,
			macd,
			ema,
		)

	switch ai.Raw {
	case Buy:
		pyramidPass :=
			pyramid.Buy.GatePassed

		equityPass :=
			equity.BuyTrigger

		produced := equityPass

		// log.Printf(
		// 	"[TRACE] equity.buy.evaluate "+
		// 		"ai_raw=%s logic=%s legacy=%s "+
		// 		"pyramid_gate=%t equity_trigger=%t produced=%t "+
		// 		"equity=%.2f baseline=%.2f",
		// 	ai.Raw,
		// 	legacy.LogicOpinion,
		// 	legacy.Signal,
		// 	pyramidPass,
		// 	equityPass,
		// 	produced,
		// 	equity.Raw.EquityUSD,
		// 	equity.Raw.BaselineUSD,
		// )

		if !produced {
			return false
		}

		d.Signal = Buy
		d.LogicOpinion = legacy.LogicOpinion
		d.LegacySignal = legacy.Signal
		d.PyramidPass = pyramidPass
		d.PyramidReason = pyramid.Buy.Reason
		d.EquityPass = equityPass
		d.EquityReason = equity.Reason
		d.Producer = EntryProducerEquity
		d.AssignRunner = true
		d.PendingCancelPolicy = PendingSignalCancelOnOpposite
		d.ProducerReason = fmt.Sprintf(
			"equity_buy|"+
				"ai_raw=%s|logic=%s|"+
				"equity=%.2f|baseline=%.2f|"+
				"trigger_usd=%.2f|distance_usd=%.2f|"+
				"equity_trigger=%t|"+
				"spacing=%t|adverse=%t|pyramid_gate=%t|"+
				"latched=%.8f|gate_price=%.8f",
			ai.Raw,
			legacy.LogicOpinion,
			equity.Raw.EquityUSD,
			equity.Raw.BaselineUSD,
			equity.Raw.BuyTriggerUSD,
			equity.Raw.BuyThresholdDistanceUSD,
			equityPass,
			pyramid.Buy.SpacingPass,
			pyramid.Buy.AdversePass,
			pyramidPass,
			pyramid.Buy.Latched,
			pyramid.Buy.EffectiveGatePrice,
		)

		return true

	case Sell:
		pyramidPass :=
			pyramid.Sell.GatePassed

		equityPass :=
			equity.SellTrigger

		produced := equityPass

		// log.Printf(
		// 	"[TRACE] equity.sell.evaluate "+
		// 		"ai_raw=%s logic=%s legacy=%s "+
		// 		"pyramid_gate=%t equity_trigger=%t produced=%t "+
		// 		"equity=%.2f baseline=%.2f",
		// 	ai.Raw,
		// 	legacy.LogicOpinion,
		// 	legacy.Signal,
		// 	pyramidPass,
		// 	equityPass,
		// 	produced,
		// 	equity.Raw.EquityUSD,
		// 	equity.Raw.BaselineUSD,
		// )

		if !produced {
			return false
		}

		d.Signal = Sell
		d.LogicOpinion = legacy.LogicOpinion
		d.LegacySignal = legacy.Signal

		d.PyramidPass = pyramidPass
		d.PyramidReason = pyramid.Sell.Reason

		d.EquityPass = equityPass
		d.EquityReason = equity.Reason
		d.PendingCancelPolicy = PendingSignalCancelOnOpposite
		d.Producer = EntryProducerEquity
		d.AssignRunner = true
		d.ProducerReason = fmt.Sprintf(
			"equity_sell|"+
				"ai_raw=%s|logic=%s|"+
				"equity=%.2f|baseline=%.2f|"+
				"trigger_usd=%.2f|distance_usd=%.2f|"+
				"equity_trigger=%t|"+
				"spacing=%t|adverse=%t|pyramid_gate=%t|"+
				"latched=%.8f|gate_price=%.8f",
			ai.Raw,
			legacy.LogicOpinion,
			equity.Raw.EquityUSD,
			equity.Raw.BaselineUSD,
			equity.Raw.SellTriggerUSD,
			equity.Raw.SellThresholdDistanceUSD,
			equityPass,
			pyramid.Sell.SpacingPass,
			pyramid.Sell.AdversePass,
			pyramidPass,
			pyramid.Sell.Latched,
			pyramid.Sell.EffectiveGatePrice,
		)

		return true

	default:
		// log.Printf(
		// 	"[TRACE] equity.evaluate "+
		// 		"ai_raw=%s logic=%s legacy=%s produced=false",
		// 	ai.Raw,
		// 	legacy.LogicOpinion,
		// 	legacy.Signal,
		// )

		return false
	}
}

// applyCase11ReversalProducer evaluates the independent Case 11
// reversal producers:
//
//   - Case 11A: Peak-Reversal SELL
//   - Case 11B: Bottom-Reversal BUY
//
// It populates all Case 11 diagnostics in d regardless of whether either
// producer fires.
//
// It returns true when Case 11 produces a directional decision.
func applyCase11ReversalProducer(
	d *EntryDecision,
	macd MACDResult,
	ema EMAPatternResult,
	pyramid PyramidResult,
) bool {
	const (
		macdPeakBuffer   = 15.0
		macdBottomBuffer = 15.0
	)

	// -------------------------------------------------------------
	// Case 11A — Peak-Reversal SELL.
	//
	// MACD[idx-6] >= EPS - buffer
	// AND EMA high-peak
	// AND Pyramid SELL gate
	// -------------------------------------------------------------
	macdPrePeakThreshold :=
		macd.EPS - macdPeakBuffer

	macdPrePeakZone :=
		macd.LinePrev6 >= macdPrePeakThreshold

	peakReversalSell :=
		macdPrePeakZone &&
			ema.HighPeak &&
			pyramid.Sell.GatePassed

	// -------------------------------------------------------------
	// Case 11B — Bottom-Reversal BUY.
	//
	// MACD[idx-6] <= -EPS + buffer
	// AND EMA low-bottom
	// AND Pyramid BUY gate
	// -------------------------------------------------------------
	macdPreBottomThreshold :=
		-macd.EPS + macdBottomBuffer

	macdPreBottomZone :=
		macd.LinePrev6 <= macdPreBottomThreshold

	bottomReversalBuy :=
		macdPreBottomZone &&
			ema.LowBottom &&
			pyramid.Buy.GatePassed

	// Always expose Case 11 raw-material interpretation in the
	// canonical EntryDecision, even when neither producer fires.
	d.MACDPrePeakZone = macdPrePeakZone
	d.PeakReversalSell = peakReversalSell
	d.MACDPreBottomZone = macdPreBottomZone
	d.BottomReversalBuy = bottomReversalBuy

	// log.Printf(
	// 	"[TRACE] case11A.peak_reversal_sell.evaluate "+
	// 		"macd_idx6=%.6f eps=%.6f buffer=%.2f threshold=%.6f "+
	// 		"macd_zone=%t ema_high_peak=%t "+
	// 		"pyramid_sell=%t pyramid_reason=%s",
	// 	macd.LinePrev6,
	// 	macd.EPS,
	// 	macdPeakBuffer,
	// 	macdPrePeakThreshold,
	// 	macdPrePeakZone,
	// 	ema.HighPeak,
	// 	pyramid.Sell.GatePassed,
	// 	pyramid.Sell.Reason,
	// )
	// log.Printf(
	// 	"[TRACE] case11B.bottom_reversal_buy.evaluate "+
	// 		"macd_idx6=%.6f eps=%.6f buffer=%.2f threshold=%.6f "+
	// 		"macd_zone=%t ema_low_bottom=%t "+
	// 		"pyramid_buy=%t pyramid_reason=%s",
	// 	macd.LinePrev6,
	// 	macd.EPS,
	// 	macdBottomBuffer,
	// 	macdPreBottomThreshold,
	// 	macdPreBottomZone,
	// 	ema.LowBottom,
	// 	pyramid.Buy.GatePassed,
	// 	pyramid.Buy.Reason,
	// )

	// Case 11A has priority over Case 11B if both somehow evaluate true.
	if peakReversalSell {
		d.Signal = Sell
		d.PyramidPass = pyramid.Sell.GatePassed
		d.PyramidReason = pyramid.Sell.Reason
		d.Producer = EntryProducerCase11APeakReversal
		d.PendingCancelPolicy = PendingSignalCancelDisabled
		d.ProducerReason = fmt.Sprintf(
			"peak_reversal_sell|"+
				"macd_idx6=%.6f|eps=%.6f|buffer=%.2f|"+
				"threshold=%.6f|"+
				"macd_zone=%t|"+
				"ema_high_peak=%t|"+
				"pyramid_sell=%t",
			macd.LinePrev6,
			macd.EPS,
			macdPeakBuffer,
			macdPrePeakThreshold,
			macdPrePeakZone,
			ema.HighPeak,
			pyramid.Sell.GatePassed,
		)

		return true
	}

	if bottomReversalBuy {
		d.Signal = Buy
		d.PyramidPass = pyramid.Buy.GatePassed
		d.PyramidReason = pyramid.Buy.Reason
		d.Producer = EntryProducerCase11BBottomReversal
		d.PendingCancelPolicy = PendingSignalCancelDisabled
		d.ProducerReason = fmt.Sprintf(
			"bottom_reversal_buy|"+
				"macd_idx6=%.6f|eps=%.6f|buffer=%.2f|"+
				"threshold=%.6f|"+
				"macd_zone=%t|"+
				"ema_low_bottom=%t|"+
				"pyramid_buy=%t",
			macd.LinePrev6,
			macd.EPS,
			macdBottomBuffer,
			macdPreBottomThreshold,
			macdPreBottomZone,
			ema.LowBottom,
			pyramid.Buy.GatePassed,
		)

		return true
	}

	return false
}

// applyCase13Producer evaluates the independent Case 13 producers.
//
// Case 13 consists of:
//
//   - Case 13A — Peak SELL producer.
//   - Case 13B — Bottom BUY producer.
//
// Both producers target early reversals from persistent trends by
// combining AI conviction, market regime, price location, MACD
// persistence, and EMA structural confirmation.
func applyCase13Producer(
	d *EntryDecision,
	ai AIResult,
	macd MACDResult,
	ema EMAPatternResult,
	pyramid PyramidResult,
	price float64,
	recentLow float64,
	recentHigh float64,
	regime MarketRegime,
	pendingCounts PendingProducerCounts,
	case13AReferencePrice float64,
) bool {
	if applyCase13APeakProducer(
		d,
		ai,
		macd,
		ema,
		pyramid,
		price,
		recentHigh,
		regime,
		pendingCounts,
		case13AReferencePrice,
	) {
		return true
	}

	if applyCase13BBottomProducer(
		d,
		ai,
		macd,
		ema,
		pyramid,
		price,
		recentLow,
		regime,
		pendingCounts,
	) {
		return true
	}

	return false
}

// applyCase13APeakProducer evaluates Case 13A, an independent Peak SELL
// producer.
//
// Case 13A targets an early reversal from a persistent bullish condition:
//
//   - AI has produced SELL with at least 0.65 confidence.
//   - Market regime remains UP.
//   - SELL-side spacing protection has passed.
//   - Live price is no more than 0.10% below the recent peak.
//   - MACD was positive at idx-6 and remains positive now.
//   - MACD histogram remains positive.
//   - EMA high-peak geometry provides the final entry trigger.
//
// The normal Pyramid SELL gate is intentionally not required. Case 13A
// uses Pyramid spacing for entry-frequency protection while proximity to
// the recent peak and EMA high-peak geometry qualify the entry location.
//
// The function always populates the Case 13A diagnostic fields in d. It
// returns true only when Case 13A emits an independent SELL decision.
func applyCase13APeakProducer(
	d *EntryDecision,
	ai AIResult,
	macd MACDResult,
	ema EMAPatternResult,
	pyramid PyramidResult,
	price float64,
	recentHigh float64,
	regime MarketRegime,
	pendingCounts PendingProducerCounts,
	case13AReferencePrice float64,
) bool {
	const (
		minConfidence        = 0.65
		maxNearPeakPct       = 0.10
		profitGateMultiplier = 0.50
		case13AReentryPct    = 0.10
	)

	case13APending :=
		pendingCounts.Count(
			EntryProducerCase13APeakSell,
			SideSell,
		)

	// Pending count is the simultaneous-duplicate guard.
	case13AAvailable :=
		case13APending == 0

	firstCase13A :=
		case13AReferencePrice <= 0

	nextCase13AReentryPrice := 0.0
	case13AReentryPass := false

	if firstCase13A {
		// First Case13A after reset retains the existing global SELL spacing.
		case13AReentryPass =
			pyramid.Sell.SpacingPass
	} else {
		nextCase13AReentryPrice =
			case13AReferencePrice *
				(1.0 + case13AReentryPct/100.0)

		// Subsequent Case13A SELLs bypass global spacing and require +0.10%.
		case13AReentryPass =
			price >= nextCase13AReentryPrice
	}

	nearPeakPct := 0.0
	priceNearRecentHigh := false

	if recentHigh > 0 && price > 0 {
		nearPeakPct =
			(recentHigh - price) /
				recentHigh * 100.0

		priceNearRecentHigh =
			nearPeakPct <= maxNearPeakPct
	}

	// Preserve Case 13A interpretation regardless of whether the
	// producer ultimately emits SELL.
	d.NearRecentHighPct = nearPeakPct
	d.PriceNearRecentHigh = priceNearRecentHigh

	// priceNearRecentHigh, confidence, regime, MACD and EMA HighPeak remain
	// normal Case13A qualification conditions. The reference mechanism only
	// replaces the duplicate/re-entry spacing behavior.
	peakSellArm :=
		case13AAvailable &&
			case13AReentryPass &&
			ai.Raw == Sell &&
			ai.Confidence >= minConfidence &&
			regime == RegimeUp &&
			priceNearRecentHigh &&
			macd.LinePrev6 > 0 &&
			macd.Line > 0 &&
			macd.Hist > 0

	// EMA high-peak geometry is the final structural confirmation.
	peakSell :=
		peakSellArm &&
			ema.HighPeak

	// log.Printf(
	// 	"[TRACE] case13A.peak_sell.evaluate "+
	// 		"ai_raw=%s confidence=%.2f min_confidence=%.2f regime=%s "+
	// 		"price=%.8f recent_peak=%.8f near_peak_pct=%.6f price_near_peak=%t "+
	// 		"macd_idx6=%.6f macd_line=%.6f macd_hist=%.6f ema_high_peak=%t "+
	// 		"pending_count=%d first=%t spacing=%t reference=%.8f next_reentry=%.8f "+
	// 		"reentry_pass=%t arm=%t producer=%t profit_gate_mult=%.2f",
	// 	ai.Raw,
	// 	ai.Confidence,
	// 	minConfidence,
	// 	regime,
	// 	price,
	// 	recentHigh,
	// 	nearPeakPct,
	// 	priceNearRecentHigh,
	// 	macd.LinePrev6,
	// 	macd.Line,
	// 	macd.Hist,
	// 	ema.HighPeak,
	// 	case13APending,
	// 	firstCase13A,
	// 	pyramid.Sell.SpacingPass,
	// 	case13AReferencePrice,
	// 	nextCase13AReentryPrice,
	// 	case13AReentryPass,
	// 	peakSellArm,
	// 	peakSell,
	// 	profitGateMultiplier,
	// )

	if !peakSell {
		return false
	}

	d.Signal = Sell
	d.PyramidReason = pyramid.Sell.Reason
	d.ProfitGateMultiplier = profitGateMultiplier
	d.Producer = EntryProducerCase13APeakSell
	d.PendingCancelPolicy = PendingSignalCancelDisabled

	referenceMode := "reference"
	if firstCase13A {
		referenceMode = "first_spacing"
	}

	d.ProducerReason = fmt.Sprintf(
		"peak_sell|"+
			"confidence=%.2f|regime=%s|"+
			"near_peak_pct=%.6f|"+
			"macd_idx6=%.6f|macd_line=%.6f|macd_hist=%.6f|"+
			"ema_high_peak=%t|"+
			"pending=%d|"+
			"reference_mode=%s|reference_price=%.8f|"+
			"next_reentry_price=%.8f|reentry_pct=%.2f|"+
			"spacing=%t|reentry_pass=%t|profit_gate_mult=%.2f",
		ai.Confidence,
		regime,
		nearPeakPct,
		macd.LinePrev6,
		macd.Line,
		macd.Hist,
		ema.HighPeak,
		case13APending,
		referenceMode,
		case13AReferencePrice,
		nextCase13AReentryPrice,
		case13AReentryPct,
		pyramid.Sell.SpacingPass,
		case13AReentryPass,
		profitGateMultiplier,
	)

	return true
}

func applyCase13BBottomProducer(
	d *EntryDecision,
	ai AIResult,
	macd MACDResult,
	ema EMAPatternResult,
	pyramid PyramidResult,
	price float64,
	recentLow float64,
	regime MarketRegime,
	pendingCounts PendingProducerCounts,
) bool {
	const (
		minConfidence        = 0.65
		maxNearLowPct        = 0.10
		profitGateMultiplier = 0.50
	)

	// Case 12 extension for Case 13B:
	//
	// The first Case13B BUY may qualify without the adverse latch.
	// While another Case13B BUY remains pending, a subsequent Case13B
	// BUY must reach the advanced BUY latch.
	case13BPending :=
		pendingCounts.Count(
			EntryProducerCase13BBottomBuy,
			SideBuy,
		)

	case13BAdverseRequired :=
		case13BPending > 0

	buyAdverseReached :=
		pyramid.Buy.Latched > 0 &&
			price <= pyramid.Buy.Latched

	case13BAdversePass :=
		!case13BAdverseRequired ||
			buyAdverseReached

	nearLowPct := 0.0
	priceNearRecentLow := false

	if recentLow > 0 && price > 0 {
		nearLowPct =
			(price - recentLow) /
				recentLow * 100.0

		priceNearRecentLow =
			nearLowPct <= maxNearLowPct
	}

	// Preserve Case 13B interpretation regardless of whether the
	// producer ultimately emits BUY.
	d.NearRecentLowPct = nearLowPct
	d.PriceNearRecentLow = priceNearRecentLow

	// The arm identifies the complete bottom environment.
	//
	// Case13B normally requires only Pyramid BUY spacing. However, when
	// another Case13B BUY is already pending, it additionally requires
	// price to reach the BUY latch advanced during pending registration.
	bottomBuyArm :=
		ai.Raw == Buy &&
			ai.Confidence >= minConfidence &&
			regime == RegimeDown &&
			pyramid.Buy.SpacingPass &&
			case13BAdversePass &&
			priceNearRecentLow &&
			macd.LinePrev6 < 0 &&
			macd.Line < 0 &&
			macd.Hist < 0

	// EMA low-bottom geometry is the final structural confirmation.
	bottomBuy :=
		bottomBuyArm &&
			ema.LowBottom

	// log.Printf(
	// 	"[TRACE] case13B.bottom_buy.evaluate "+
	// 		"ai_raw=%s confidence=%.2f min_confidence=%.2f regime=%s "+
	// 		"price=%.8f recent_low=%.8f near_low_pct=%.6f "+
	// 		"max_near_low_pct=%.2f price_near_low=%t "+
	// 		"macd_idx6=%.6f macd_line=%.6f macd_hist=%.6f "+
	// 		"ema_low_bottom=%t "+
	// 		"pyramid_buy_spacing=%t "+
	// 		"pending_count=%d adverse_required=%t "+
	// 		"buy_latched=%.8f adverse_reached=%t adverse_pass=%t|profit_gate_mult=%.2f"+
	// 		"arm=%t producer=%t ",
	// 	ai.Raw,
	// 	ai.Confidence,
	// 	minConfidence,
	// 	regime,
	// 	price,
	// 	recentLow,
	// 	nearLowPct,
	// 	maxNearLowPct,
	// 	priceNearRecentLow,
	// 	macd.LinePrev6,
	// 	macd.Line,
	// 	macd.Hist,
	// 	ema.LowBottom,
	// 	pyramid.Buy.SpacingPass,
	// 	case13BPending,
	// 	case13BAdverseRequired,
	// 	pyramid.Buy.Latched,
	// 	buyAdverseReached,
	// 	case13BAdversePass,
	// 	bottomBuyArm,
	// 	bottomBuy,
	// 	profitGateMultiplier,
	// )

	if !bottomBuy {
		return false
	}

	d.Signal = Buy

	// Case13B requires BUY spacing. The complete ordinary Pyramid gate
	// is not required for the first entry. Its advanced latch is required
	// only when another Case13B BUY is already pending.
	// d.PyramidPass =
	// 	pyramid.Buy.SpacingPass &&
	// 		case13BAdversePass
	d.PyramidReason = pyramid.Buy.Reason
	d.Producer = EntryProducerCase13BBottomBuy
	d.PendingCancelPolicy = PendingSignalCancelDisabled
	d.ProfitGateMultiplier = profitGateMultiplier
	d.ProducerReason = fmt.Sprintf(
		"bottom_buy|"+
			"confidence=%.2f|regime=%s|"+
			"price=%.8f|recent_low=%.8f|near_low_pct=%.6f|"+
			"macd_idx6=%.6f|macd_line=%.6f|macd_hist=%.6f|"+
			"ema_low_bottom=%t|spacing=%t|"+
			"pending=%d|adverse_required=%t|"+
			"buy_latched=%.8f|adverse_reached=%t|adverse_pass=%t|profit_gate_mult=%.2f",
		ai.Confidence,
		regime,
		price,
		recentLow,
		nearLowPct,
		macd.LinePrev6,
		macd.Line,
		macd.Hist,
		ema.LowBottom,
		pyramid.Buy.SpacingPass,
		case13BPending,
		case13BAdverseRequired,
		pyramid.Buy.Latched,
		buyAdverseReached,
		case13BAdversePass,
		profitGateMultiplier,
	)

	return true
}

// Case14BUptrendBuyProducer eases the normal BUY adverse-price gate during
// strong UP-trend continuation.
//
// This producer owns only the buffered window immediately above the BUY
// latch and never overlaps with NormalLegacy.
//
// Territory:
//
//	price <= BUY latch
//	    -> NormalLegacy
//
//	BUY latch < price <= buffered BUY latch
//	    -> Case14B (reduced profit target)
//
//	price > buffered BUY latch
//	    -> No Case14B entry
//
//	pending Case14B > 0
//	    -> Case14B disabled
//
// Requires AI BUY, Legacy BUY, Logic BUY, BUY pattern, UP regime, and
// BUY spacing gate.
func applyCase14BUptrendBuyProducer(
	d *EntryDecision,
	ai AIResult,
	macd MACDResult,
	ema EMAPatternResult,
	pyramid PyramidResult,
	price float64,
	regime MarketRegime,
	pendingCounts PendingProducerCounts,
) bool {
	legacy :=
		evaluateLegacyDirection(
			ai,
			macd,
			ema,
		)
	const (
		minConfidence        = 0.30
		nearLatchBufferPct   = 0.56
		profitGateMultiplier = 0.50
	)

	case14BPending :=
		pendingCounts.Count(
			EntryProducerCase14BUptrendBuy,
			SideBuy,
		)

	latchValid :=
		pyramid.Buy.Latched > 0

	actualLatchReached :=
		latchValid &&
			price <= pyramid.Buy.Latched

	bufferedLatch := 0.0

	if latchValid {
		bufferedLatch =
			pyramid.Buy.Latched *
				(1.0 + nearLatchBufferPct/100.0)
	}

	// case14B owns only the narrow window immediately above the BUY latch.
	//
	// At or below the latch, NormalLegacy owns the entry.
	// Above the buffered latch, the entry is too early.
	withinLatchWindow :=
		latchValid &&
			!actualLatchReached &&
			price <= bufferedLatch

	// A pending case14B entry disables this producer completely.
	case14BAvailable :=
		case14BPending == 0 &&
			withinLatchWindow

	uptrendBuy :=
		case14BAvailable &&
			ai.Raw == Buy &&
			ai.Confidence >= minConfidence &&
			legacy.Signal == Buy &&
			legacy.LogicOpinion == Buy &&
			regime == RegimeUp &&
			ema.PatternBuy &&
			pyramid.Buy.SpacingPass

	// log.Printf(
	// 	"[TRACE] case14B.uptrend_buy.evaluate "+
	// 		"ai_raw=%s confidence=%.2f min_confidence=%.2f "+
	// 		"legacy=%s logic=%s regime=%s pattern_buy=%t "+
	// 		"price=%.8f latch=%.8f buffered_latch=%.8f "+
	// 		"buffer_pct=%.4f actual_latch_reached=%t "+
	// 		"within_latch_window=%t spacing=%t "+
	// 		"pending_count=%d available=%t "+
	// 		"profit_gate_mult=%.2f produced=%t",
	// 	ai.Raw,
	// 	ai.Confidence,
	// 	minConfidence,
	// 	legacy.Signal,
	// 	legacy.LogicOpinion,
	// 	regime,
	// 	ema.PatternBuy,
	// 	price,
	// 	pyramid.Buy.Latched,
	// 	bufferedLatch,
	// 	nearLatchBufferPct,
	// 	actualLatchReached,
	// 	withinLatchWindow,
	// 	pyramid.Buy.SpacingPass,
	// 	case14BPending,
	// 	case14BAvailable,
	// 	profitGateMultiplier,
	// 	uptrendBuy,
	// )

	if !uptrendBuy {
		return false
	}

	d.Signal = Buy
	d.ProfitGateMultiplier = profitGateMultiplier
	d.PyramidReason = pyramid.Buy.Reason
	d.Producer = EntryProducerCase14BUptrendBuy
	d.PendingCancelPolicy = PendingSignalCancelDisabled
	d.ProducerReason = fmt.Sprintf(
		"uptrend_buffered_latch_buy|"+
			"confidence=%.2f|"+
			"regime=%s|"+
			"price=%.8f|"+
			"latch=%.8f|"+
			"buffered_latch=%.8f|"+
			"actual_latch=%t|"+
			"within_window=%t|"+
			"spacing=%t|"+
			"pending=%d|"+
			"legacy=%s|"+
			"logic=%s|"+
			"pattern_buy=%t|"+
			"profit_gate_mult=%.2f",
		ai.Confidence,
		regime,
		price,
		pyramid.Buy.Latched,
		bufferedLatch,
		actualLatchReached,
		withinLatchWindow,
		pyramid.Buy.SpacingPass,
		case14BPending,
		legacy.Signal,
		legacy.LogicOpinion,
		ema.PatternBuy,
		profitGateMultiplier,
	)

	return true
}

type LegacyDirectionResult struct {
	NormalBuy  bool
	NormalSell bool

	LogicOpinion Signal
	Signal       Signal
}

func evaluateLegacyDirection(
	ai AIResult,
	macd MACDResult,
	ema EMAPatternResult,
) LegacyDirectionResult {
	result := LegacyDirectionResult{
		NormalBuy: macd.StrongNegative &&
			macd.MomentumUp &&
			ema.PatternBuy,

		NormalSell: macd.StrongPositive &&
			macd.MomentumDown &&
			ema.PatternSell,
	}

	switch {
	case result.NormalBuy:
		result.LogicOpinion = Buy

	case result.NormalSell:
		result.LogicOpinion = Sell

	default:
		result.LogicOpinion = Flat
	}

	result.Signal =
		finalSignalFromAILogic(
			ai.Raw,
			result.LogicOpinion,
		)

	return result
}

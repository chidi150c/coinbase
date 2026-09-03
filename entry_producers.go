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
//	Bottom Reversal, Capitulation Bottom, Momentum Exhaustion, etc.). Ordinary producers are
//	evaluated independently; every qualifying directional decision is retained for downstream
//	resource coordination rather than being suppressed by first-match return behavior.
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
// Resource Priority
//
//	ProducerPriority is intentionally separate from ProducerTier. ProducerTier controls
//	producer economics / profit-target behavior. ProducerPriority controls only downstream
//	resource-allocation precedence when multiple qualifying producer requests compete for
//	constrained resources.
//
//	All ordinary producers remain independently evaluable. No producer is skipped merely
//	because a higher-priority producer also fired. Equal-priority producers are expected to
//	share constrained resources proportionally in the Producer Resource Coordinator.
//
//	Case3AReplacement has the highest priority but retains its special exit-owned lifecycle;
//	this priority does not change EXIT -> OPEN sequencing in step(). Existing pending-order
//	reservations remain paramount and are never revoked by a newly evaluated priority.
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
	"strings"
	"time"
)

type EntryProducer string

// ProducerPriority controls resource-allocation precedence only.
// Higher numeric values are allocated before lower values when resources
// are constrained. It is deliberately independent of ProducerTier.
type ProducerPriority int

const (
	ProducerPriorityCase3AReplacement ProducerPriority = 800
	ProducerPriorityCase11A           ProducerPriority = 700
	ProducerPriorityCase11B           ProducerPriority = 600
	ProducerPriorityCase13A           ProducerPriority = 500
	ProducerPriorityCase13B           ProducerPriority = 400
	ProducerPriorityCase14B           ProducerPriority = 300
	ProducerPriorityEquity            ProducerPriority = 200
	ProducerPriorityNormalLegacy      ProducerPriority = 100
)

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

// producerPriorityFor is the single authoritative mapping used by resource
// coordination. Do not derive allocation priority from ProducerTier.
func producerPriorityFor(producer EntryProducer) ProducerPriority {
	switch producer {
	case EntryProducerCase3AReplacement:
		return ProducerPriorityCase3AReplacement

	case EntryProducerCase11APeakReversal:
		return ProducerPriorityCase11A

	case EntryProducerCase11BBottomReversal:
		return ProducerPriorityCase11B

	case EntryProducerCase13APeakSell:
		return ProducerPriorityCase13A

	case EntryProducerCase13BBottomBuy:
		return ProducerPriorityCase13B

	case EntryProducerCase14BUptrendBuy:
		return ProducerPriorityCase14B

	case EntryProducerEquity:
		return ProducerPriorityEquity

	case EntryProducerNormalLegacy:
		return ProducerPriorityNormalLegacy

	default:
		return 0
	}
}

// ProducerTier standardizes ordinary producer profit-target power levels.
// Case3AReplacement is intentionally exempt because it owns special recovery
// accounting and assigns its recovery target outside the ordinary tier pipeline.
type ProducerTier string

const (
	ProducerTierHigh ProducerTier = "HIGH"
	ProducerTierMid  ProducerTier = "MID"
	ProducerTierLow  ProducerTier = "LOW"
)

const (
	HighTierProducerMultiplier = 1.00
	MidTierProducerMultiplier  = 0.75
	LowTierProducerMultiplier  = 0.50

	// ContinuationProfitGateFactor reduces the producer portion of the exit
	// target by 20% for continuation entries. Recovery additions remain outside
	// this multiplier in step.go.
	ContinuationProfitGateFactor = 0.80

	// ContinuationEntrySpacingPct is the universal same-producer/same-side
	// committed-reference spacing used by ordinary continuation entries.
	ContinuationEntrySpacingPct = 0.20
)

// ProducerContinuationReferences stores the last committed reference for every
// ordinary producer and side. For market-price producers the value is the
// committed execution price. Equity intentionally uses committed account equity
// as its reference unit.
type ProducerContinuationReferences map[EntryProducer]map[OrderSide]float64

func (r ProducerContinuationReferences) Reference(
	producer EntryProducer,
	side OrderSide,
) float64 {
	if r == nil {
		return 0
	}
	bySide := r[producer]
	if bySide == nil {
		return 0
	}
	ref := bySide[side]
	if ref <= 0 {
		return 0
	}
	return ref
}

func producerTierFor(producer EntryProducer) (ProducerTier, float64) {
	switch producer {
	case EntryProducerNormalLegacy,
		EntryProducerEquity:
		return ProducerTierHigh, HighTierProducerMultiplier

	case EntryProducerCase11APeakReversal,
		EntryProducerCase11BBottomReversal,
		EntryProducerCase14BUptrendBuy:
		return ProducerTierMid, MidTierProducerMultiplier

	case EntryProducerCase13APeakSell,
		EntryProducerCase13BBottomBuy:
		return ProducerTierLow, LowTierProducerMultiplier

	case EntryProducerCase3AReplacement:
		panic("producerTierFor: Case3AReplacement is a special-case exemption")

	default:
		panic(fmt.Sprintf("producerTierFor: unsupported producer %q", producer))
	}
}

func continuationEntryMultiplier(
	side OrderSide,
) float64 {
	spacing :=
		ContinuationEntrySpacingPct /
			100.0

	switch side {
	case SideSell:
		return 1.0 + spacing

	case SideBuy:
		return 1.0 - spacing

	default:
		return 0
	}
}

func continuationReferenceGate(
	side OrderSide,
	current float64,
	reference float64,
) (threshold float64, pass bool) {
	if reference <= 0 || current <= 0 {
		return 0, false
	}

	multiplier :=
		continuationEntryMultiplier(
			side,
		)

	if multiplier <= 0 {
		return 0, false
	}

	threshold =
		reference *
			multiplier

	switch side {
	case SideSell:
		return threshold, current >= threshold

	case SideBuy:
		return threshold, current <= threshold

	default:
		return 0, false
	}
}

func applyStandardProducerEconomics(
	d *EntryDecision,
	producer EntryProducer,
	continuation bool,
	reference float64,
	threshold float64,
	entryPass bool,
) {
	if d == nil {
		panic("applyStandardProducerEconomics: nil EntryDecision")
	}

	tier, tierMultiplier := producerTierFor(producer)
	resolvedMultiplier := tierMultiplier
	if continuation {
		resolvedMultiplier *= ContinuationProfitGateFactor
	}

	d.ProducerPriority = producerPriorityFor(producer)
	d.ProducerTier = tier
	d.ProducerTierMultiplier = tierMultiplier
	d.IsContinuation = continuation
	d.ContinuationReference = reference
	d.ContinuationEntryThreshold = threshold
	d.ContinuationEntryPass = entryPass
	d.ProfitGateMultiplier = resolvedMultiplier
}

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
	Signal           Signal
	Raw              Signal
	LegacySignal     Signal
	LogicOpinion     Signal
	Producer         EntryProducer
	ProducerPriority ProducerPriority
	Confidence       float64

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
	NearRecentLowPct    float64
	PriceNearRecentLow  bool
	NearRecentHighPct   float64
	PriceNearRecentHigh bool

	// Standard producer economics / continuation diagnostics.
	ProducerTier               ProducerTier
	ProducerTierMultiplier     float64
	IsContinuation             bool
	ContinuationReference      float64
	ContinuationEntryThreshold float64
	ContinuationEntryPass      bool
	ProfitGateMultiplier       float64

	ProducerReason      string
	PendingCancelPolicy PendingSignalCancelPolicy
	AssignRunner        bool
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
			ResetLastAdd:     false,
			ResetWinExtreme:  false,
			ResetLatchedGate: false,
			ResetRegime:      false,
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

// applyNormalLegacyProducer evaluates the standard AI + Logic producer.
//
// First entry:
//   - AI and Logic must resolve to BUY or SELL; and
//   - the matching complete Pyramid gate must pass.
//
// Continuation:
//   - the same AI / Logic / pattern qualification remains authoritative; and
//   - the native Pyramid admission is replaced by the standardized
//     same-producer/same-side committed-reference spacing gate.
func applyNormalLegacyProducer(
	d *EntryDecision,
	ai AIResult,
	macd MACDResult,
	ema EMAPatternResult,
	pyramid PyramidResult,
	price float64,
	continuationRefs ProducerContinuationReferences,
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
		reference :=
			continuationRefs.Reference(
				EntryProducerNormalLegacy,
				SideBuy,
			)
		continuation := reference > 0
		nextEntryPrice := 0.0
		entryPass := pyramid.Buy.GatePassed

		if continuation {
			nextEntryPrice, entryPass =
				continuationReferenceGate(
					SideBuy,
					price,
					reference,
				)
		}

		if !entryPass {
			return false
		}

		d.Signal = Buy
		d.LogicOpinion = legacy.LogicOpinion
		d.LegacySignal = legacy.Signal
		d.PyramidPass = pyramid.Buy.GatePassed
		d.PyramidReason = pyramid.Buy.Reason
		d.Producer = EntryProducerNormalLegacy
		d.PendingCancelPolicy = PendingSignalCancelOnFlatOrOpposite

		applyStandardProducerEconomics(
			d,
			EntryProducerNormalLegacy,
			continuation,
			reference,
			nextEntryPrice,
			entryPass,
		)

		d.ProducerReason = fmt.Sprintf(
			"normal_legacy_buy|"+
				"ai_raw=%s|logic=%s|"+
				"strong_negative=%t|momentum_up=%t|pattern_buy=%t|"+
				"spacing=%t|adverse=%t|pyramid_gate=%t|"+
				"latched=%.8f|gate_price=%.8f|"+
				"tier=%s|tier_mult=%.6f|continuation=%t|"+
				"continuation_reference=%.8f|next_entry_price=%.8f|"+
				"continuation_spacing_pct=%.4f|entry_gate_pass=%t|"+
				"continuation_profit_factor=%.6f|profit_gate_mult=%.6f",
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
			d.ProducerTier,
			d.ProducerTierMultiplier,
			d.IsContinuation,
			d.ContinuationReference,
			d.ContinuationEntryThreshold,
			ContinuationEntrySpacingPct,
			d.ContinuationEntryPass,
			ContinuationProfitGateFactor,
			d.ProfitGateMultiplier,
		)

		return true

	case Sell:
		reference :=
			continuationRefs.Reference(
				EntryProducerNormalLegacy,
				SideSell,
			)
		continuation := reference > 0
		nextEntryPrice := 0.0
		entryPass := pyramid.Sell.GatePassed

		if continuation {
			nextEntryPrice, entryPass =
				continuationReferenceGate(
					SideSell,
					price,
					reference,
				)
		}

		if !entryPass {
			return false
		}

		d.Signal = Sell
		d.LogicOpinion = legacy.LogicOpinion
		d.LegacySignal = legacy.Signal
		d.PyramidPass = pyramid.Sell.GatePassed
		d.PyramidReason = pyramid.Sell.Reason
		d.Producer = EntryProducerNormalLegacy
		d.PendingCancelPolicy = PendingSignalCancelOnFlatOrOpposite

		applyStandardProducerEconomics(
			d,
			EntryProducerNormalLegacy,
			continuation,
			reference,
			nextEntryPrice,
			entryPass,
		)

		d.ProducerReason = fmt.Sprintf(
			"normal_legacy_sell|"+
				"ai_raw=%s|logic=%s|"+
				"strong_positive=%t|momentum_down=%t|pattern_sell=%t|"+
				"spacing=%t|adverse=%t|pyramid_gate=%t|"+
				"latched=%.8f|gate_price=%.8f|"+
				"tier=%s|tier_mult=%.6f|continuation=%t|"+
				"continuation_reference=%.8f|next_entry_price=%.8f|"+
				"continuation_spacing_pct=%.4f|entry_gate_pass=%t|"+
				"continuation_profit_factor=%.6f|profit_gate_mult=%.6f",
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
			d.ProducerTier,
			d.ProducerTierMultiplier,
			d.IsContinuation,
			d.ContinuationReference,
			d.ContinuationEntryThreshold,
			ContinuationEntrySpacingPct,
			d.ContinuationEntryPass,
			ContinuationProfitGateFactor,
			d.ProfitGateMultiplier,
		)

		return true

	default:
		return false
	}
}

// newProducerDecisionLifecycle creates the lifecycle state for a qualifying
// producer decision. The resulting DecisionID exists before resource allocation
// so allocation_requested / approved / partial / rejected can remain on the same
// producer attempt even when no exchange OrderID is ever created.
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
	resourceSnapshot ResourceSnapshot,
	continuationRefs ProducerContinuationReferences,
) (EquityResult, error) {
	equityRaw := t.evaluateEquityRaw()

	equityBuyReference :=
		continuationRefs.Reference(
			EntryProducerEquity,
			SideBuy,
		)
	equitySellReference :=
		continuationRefs.Reference(
			EntryProducerEquity,
			SideSell,
		)

	// Equity keeps its own signal intelligence. Continuation only replaces the
	// native Equity threshold with the standardized same-producer/same-side
	// committed-reference admission gate. Equity references are denominated in
	// account-equity USD, not market execution price.
	equityBuyContinuationActive :=
		equityBuyReference > 0 &&
			ai.Raw != Sell
	equitySellContinuationActive :=
		equitySellReference > 0 &&
			ai.Raw != Buy

	if equityBuyContinuationActive {
		nextBuyEquity, buyPass :=
			continuationReferenceGate(
				SideBuy,
				equityRaw.EquityUSD,
				equityBuyReference,
			)

		// In continuation mode this is the effective trigger multiplier
		// relative to the committed Equity reference, not the configured
		// first-entry Equity multiplier.
		equityRaw.BuyTriggerMult =
			continuationEntryMultiplier(
				SideBuy,
			)
		equityRaw.BuyTriggerUSD = nextBuyEquity
		equityRaw.BuyThresholdDistanceUSD =
			nextBuyEquity -
				equityRaw.EquityUSD
		equityRaw.BuyThresholdPassed = buyPass
	}

	if equitySellContinuationActive {
		nextSellEquity, sellPass :=
			continuationReferenceGate(
				SideSell,
				equityRaw.EquityUSD,
				equitySellReference,
			)

		// Mirror BUY: continuation exposes +0.20% as the effective
		// reference-relative Equity trigger multiplier.
		equityRaw.SellTriggerMult =
			continuationEntryMultiplier(
				SideSell,
			)
		equityRaw.SellTriggerUSD = nextSellEquity
		equityRaw.SellThresholdDistanceUSD =
			equityRaw.EquityUSD -
				nextSellEquity
		equityRaw.SellThresholdPassed = sellPass
	}

	/*
		Equity owns its own direction.

		BUY:
			current Equity has crossed the configured BUY Equity threshold.

		SELL:
			current Equity has crossed the configured SELL Equity threshold.

		AI, MACD, EMA, Legacy, and Pyramid do not determine Equity direction.
	*/
	equitySignal := Flat

	switch {
	case equityRaw.BuyThresholdPassed &&
		equityRaw.SellThresholdPassed:

		return EquityResult{}, fmt.Errorf(
			"ambiguous equity thresholds: "+
				"equity=%.8f baseline=%.8f "+
				"buy_trigger=%.8f sell_trigger=%.8f",
			equityRaw.EquityUSD,
			equityRaw.BaselineUSD,
			equityRaw.BuyTriggerUSD,
			equityRaw.SellTriggerUSD,
		)

	case equityRaw.BuyThresholdPassed:
		equitySignal = Buy

	case equityRaw.SellThresholdPassed:
		equitySignal = Sell
	}

	// Funding material comes from the ONE immutable ResourceSnapshot built for
	// this entry-allocation cycle. Equity no longer performs a private balance
	// lookup, so every producer sees the same balance timestamp and the same
	// already-netted pending reservations.
	quoteStep := resourceSnapshot.QuoteStep
	baseStep := resourceSnapshot.BaseStep
	spareQuote := resourceSnapshot.SpareQuote
	spareBase := resourceSnapshot.SpareBase

	// Resource availability is diagnostic here. It must not suppress the Equity
	// signal; the Producer Resource Coordinator owns final funding admission.
	if equitySignal == Buy && quoteStep <= 0 {
		quoteStep = 0
	}
	if equitySignal == Sell && baseStep <= 0 {
		baseStep = 0
	}

	/*
		interpretEquityRaw historically names this directional input
		legacySignal.

		For Equity it is now the Equity-owned direction. This preserves
		the existing EquityResult structure without restoring a dependency
		on Legacy.
	*/
	equityResult :=
		interpretEquityRaw(
			equityRaw,
			equitySignal,
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

	// Preserve the existing Equity reason contract and extend it additively so
	// BOT OPS / forensic logs can tell whether the reduced SELL threshold was
	// authoritative for this evaluation.
	equityResult.Reason = strings.TrimSpace(
		equityResult.Reason,
	)

	continuationReason := fmt.Sprintf(
		"buy_continuation=%t|buy_continuation_reference=%.8f|"+
			"sell_continuation=%t|sell_continuation_reference=%.8f|"+
			"continuation_spacing_pct=%.4f",
		equityBuyContinuationActive,
		equityBuyReference,
		equitySellContinuationActive,
		equitySellReference,
		ContinuationEntrySpacingPct,
	)

	if equityResult.Reason == "" {
		equityResult.Reason = continuationReason
	} else {
		equityResult.Reason += "|" + continuationReason
	}

	return equityResult, nil
}

// applyEquityProducer evaluates the independent Equity producer.
//
// Equity owns its entry direction:
//
//   - BUY when the BUY Equity threshold passes.
//   - SELL when the SELL Equity threshold passes.
//
// Funding fields remain diagnostics derived from the common ResourceSnapshot.
// Final funding admission belongs exclusively to ProducerResourceCoordinator.
//
// First Equity BUY/SELL entries use the ordinary configured Equity thresholds.
// After a committed same-side Equity entry establishes a continuation reference,
// later entries use the standardized +/-0.20% account-equity reference gate.
// Opposite AI state remains the mirrored episode-cancellation condition in the
// Trader-owned decision path.
//
// AI, Logic, Legacy, and Pyramid are retained as diagnostics only.
// They do not gate an Equity-triggered entry.
func applyEquityProducer(
	d *EntryDecision,
	ai AIResult,
	macd MACDResult,
	ema EMAPatternResult,
	pyramid PyramidResult,
	equity EquityResult,
	pendingCounts PendingProducerCounts,
	continuationRefs ProducerContinuationReferences,
) bool {
	if d == nil {
		return false
	}

	equityBuyPending :=
		pendingCounts.Count(
			EntryProducerEquity,
			SideBuy,
		)

	equitySellPending :=
		pendingCounts.Count(
			EntryProducerEquity,
			SideSell,
		)

	// Equity remains single-flight across both directions.
	equityAvailable :=
		equityBuyPending == 0 &&
			equitySellPending == 0

	if !equityAvailable {
		return false
	}

	legacy :=
		evaluateLegacyDirection(
			ai,
			macd,
			ema,
		)

	switch {
	case equity.BuyTrigger:
		reference :=
			continuationRefs.Reference(
				EntryProducerEquity,
				SideBuy,
			)
		continuation :=
			reference > 0 &&
				ai.Raw != Sell
		nextEntryEquity := 0.0
		entryPass := equity.BuyTrigger

		if continuation {
			nextEntryEquity, entryPass =
				continuationReferenceGate(
					SideBuy,
					equity.Raw.EquityUSD,
					reference,
				)
		}

		if !entryPass {
			return false
		}

		d.Signal = Buy
		d.LogicOpinion = legacy.LogicOpinion
		d.LegacySignal = legacy.Signal
		d.PyramidPass = pyramid.Buy.GatePassed
		d.PyramidReason = pyramid.Buy.Reason
		d.EquityPass = true
		d.EquityReason = equity.Reason
		d.Producer = EntryProducerEquity
		d.AssignRunner = true
		d.PendingCancelPolicy = PendingSignalCancelOnOpposite

		applyStandardProducerEconomics(
			d,
			EntryProducerEquity,
			continuation,
			reference,
			nextEntryEquity,
			entryPass,
		)

		d.ProducerReason = fmt.Sprintf(
			"equity_buy|"+
				"ai_raw=%s|logic=%s|legacy=%s|"+
				"equity=%.2f|baseline=%.2f|"+
				"trigger_usd=%.2f|distance_usd=%.2f|"+
				"threshold_pass=%t|funding_pass=%t|"+
				"raw_spare_quote=%.8f|spare_quote=%.8f|"+
				"proposed_buy_quote=%.8f|"+
				"equity_pending_buy=%d|equity_pending_sell=%d|"+
				"spacing=%t|adverse=%t|pyramid_gate=%t|"+
				"latched=%.8f|gate_price=%.8f|"+
				"tier=%s|tier_mult=%.6f|continuation=%t|"+
				"continuation_reference=%.8f|next_entry_equity=%.8f|"+
				"continuation_spacing_pct=%.4f|entry_gate_pass=%t|"+
				"continuation_profit_factor=%.6f|profit_gate_mult=%.6f",
			ai.Raw,
			legacy.LogicOpinion,
			legacy.Signal,
			equity.Raw.EquityUSD,
			equity.Raw.BaselineUSD,
			equity.Raw.BuyTriggerUSD,
			equity.Raw.BuyThresholdDistanceUSD,
			equity.Raw.BuyThresholdPassed,
			equity.BuyFundingAvailable,
			equity.RawSpareQuote,
			equity.SpareQuote,
			equity.ProposedBuyQuote,
			equityBuyPending,
			equitySellPending,
			pyramid.Buy.SpacingPass,
			pyramid.Buy.AdversePass,
			pyramid.Buy.GatePassed,
			pyramid.Buy.Latched,
			pyramid.Buy.EffectiveGatePrice,
			d.ProducerTier,
			d.ProducerTierMultiplier,
			d.IsContinuation,
			d.ContinuationReference,
			d.ContinuationEntryThreshold,
			ContinuationEntrySpacingPct,
			d.ContinuationEntryPass,
			ContinuationProfitGateFactor,
			d.ProfitGateMultiplier,
		)

		return true

	case equity.SellTrigger:
		reference :=
			continuationRefs.Reference(
				EntryProducerEquity,
				SideSell,
			)
		continuation :=
			reference > 0 &&
				ai.Raw != Buy
		nextEntryEquity := 0.0
		entryPass := equity.SellTrigger

		if continuation {
			nextEntryEquity, entryPass =
				continuationReferenceGate(
					SideSell,
					equity.Raw.EquityUSD,
					reference,
				)
		}

		if !entryPass {
			return false
		}

		d.Signal = Sell
		d.LogicOpinion = legacy.LogicOpinion
		d.LegacySignal = legacy.Signal
		d.PyramidPass = pyramid.Sell.GatePassed
		d.PyramidReason = pyramid.Sell.Reason
		d.EquityPass = true
		d.EquityReason = equity.Reason
		d.Producer = EntryProducerEquity
		d.AssignRunner = true
		d.PendingCancelPolicy = PendingSignalCancelOnOpposite

		applyStandardProducerEconomics(
			d,
			EntryProducerEquity,
			continuation,
			reference,
			nextEntryEquity,
			entryPass,
		)

		d.ProducerReason = fmt.Sprintf(
			"equity_sell|"+
				"ai_raw=%s|logic=%s|legacy=%s|"+
				"equity=%.2f|baseline=%.2f|"+
				"trigger_usd=%.2f|distance_usd=%.2f|"+
				"threshold_pass=%t|funding_pass=%t|"+
				"raw_spare_base=%.8f|spare_base=%.8f|"+
				"proposed_sell_base=%.8f|"+
				"equity_pending_buy=%d|equity_pending_sell=%d|"+
				"spacing=%t|adverse=%t|pyramid_gate=%t|"+
				"latched=%.8f|gate_price=%.8f|"+
				"tier=%s|tier_mult=%.6f|continuation=%t|"+
				"continuation_reference=%.8f|next_entry_equity=%.8f|"+
				"continuation_spacing_pct=%.4f|entry_gate_pass=%t|"+
				"continuation_profit_factor=%.6f|profit_gate_mult=%.6f",
			ai.Raw,
			legacy.LogicOpinion,
			legacy.Signal,
			equity.Raw.EquityUSD,
			equity.Raw.BaselineUSD,
			equity.Raw.SellTriggerUSD,
			equity.Raw.SellThresholdDistanceUSD,
			equity.Raw.SellThresholdPassed,
			equity.SellFundingAvailable,
			equity.RawSpareBase,
			equity.SpareBase,
			equity.ProposedSellBase,
			equityBuyPending,
			equitySellPending,
			pyramid.Sell.SpacingPass,
			pyramid.Sell.AdversePass,
			pyramid.Sell.GatePassed,
			pyramid.Sell.Latched,
			pyramid.Sell.EffectiveGatePrice,
			d.ProducerTier,
			d.ProducerTierMultiplier,
			d.IsContinuation,
			d.ContinuationReference,
			d.ContinuationEntryThreshold,
			ContinuationEntrySpacingPct,
			d.ContinuationEntryPass,
			ContinuationProfitGateFactor,
			d.ProfitGateMultiplier,
		)

		return true

	default:
		return false
	}
}

// applyCase11APeakReversalProducer evaluates Case 11A independently.
//
// Case11A is a Peak-Reversal SELL producer. First entry requires the complete
// Pyramid SELL gate; continuation keeps the MACD/EMA signal qualification but
// replaces native Pyramid admission with the standardized +0.20% committed-
// reference gate.
func applyCase11APeakReversalProducer(
	d *EntryDecision,
	ai AIResult,
	macd MACDResult,
	ema EMAPatternResult,
	pyramid PyramidResult,
	price float64,
	continuationRefs ProducerContinuationReferences,
) bool {
	if d == nil {
		return false
	}

	const macdPeakBuffer = 15.0

	reference :=
		continuationRefs.Reference(
			EntryProducerCase11APeakReversal,
			SideSell,
		)

	macdPrePeakThreshold :=
		macd.EPS - macdPeakBuffer
	macdPrePeakZone :=
		macd.LinePrev6 >= macdPrePeakThreshold

	continuation := reference > 0
	nextEntryPrice := 0.0
	entryGatePass := pyramid.Sell.GatePassed

	if continuation {
		nextEntryPrice, entryGatePass =
			continuationReferenceGate(
				SideSell,
				price,
				reference,
			)
	}

	confidence := ai.Confidence
	confidenceFallback := false
	if confidence <= 0 {
		confidence = 1.0
		confidenceFallback = true
	}

	peakReversalSell :=
		macdPrePeakZone &&
			ema.HighPeak &&
			entryGatePass

	d.MACDPrePeakZone = macdPrePeakZone
	d.PeakReversalSell = peakReversalSell

	if !peakReversalSell {
		return false
	}

	d.Signal = Sell
	d.Confidence = confidence
	d.PyramidPass = pyramid.Sell.GatePassed
	d.PyramidReason = pyramid.Sell.Reason
	d.Producer = EntryProducerCase11APeakReversal
	d.PendingCancelPolicy = PendingSignalCancelDisabled

	applyStandardProducerEconomics(
		d,
		EntryProducerCase11APeakReversal,
		continuation,
		reference,
		nextEntryPrice,
		entryGatePass,
	)

	d.ProducerReason = fmt.Sprintf(
		"peak_reversal_sell|"+
			"ai_raw=%s|ai_confidence=%.6f|case11a_confidence=%.6f|confidence_fallback=%t|"+
			"macd_idx6=%.6f|eps=%.6f|buffer=%.2f|threshold=%.6f|"+
			"macd_zone=%t|ema_high_peak=%t|"+
			"reference_mode=%s|reference_price=%.8f|next_entry_price=%.8f|"+
			"continuation_spacing_pct=%.4f|entry_gate_pass=%t|pyramid_sell=%t|"+
			"tier=%s|tier_mult=%.6f|continuation=%t|"+
			"continuation_profit_factor=%.6f|profit_gate_mult=%.6f",
		ai.Raw,
		ai.Confidence,
		confidence,
		confidenceFallback,
		macd.LinePrev6,
		macd.EPS,
		macdPeakBuffer,
		macdPrePeakThreshold,
		macdPrePeakZone,
		ema.HighPeak,
		map[bool]string{true: "continuation_reference", false: "first_pyramid"}[continuation],
		reference,
		nextEntryPrice,
		ContinuationEntrySpacingPct,
		entryGatePass,
		pyramid.Sell.GatePassed,
		d.ProducerTier,
		d.ProducerTierMultiplier,
		d.IsContinuation,
		ContinuationProfitGateFactor,
		d.ProfitGateMultiplier,
	)

	return true
}

// applyCase11BBottomReversalProducer evaluates Case 11B independently.
//
// Case11B is the BUY mirror. First entry requires the complete Pyramid BUY
// gate; continuation keeps the MACD/EMA signal qualification and replaces
// native Pyramid admission with the standardized -0.20% committed-reference
// gate.
func applyCase11BBottomReversalProducer(
	d *EntryDecision,
	ai AIResult,
	macd MACDResult,
	ema EMAPatternResult,
	pyramid PyramidResult,
	price float64,
	continuationRefs ProducerContinuationReferences,
) bool {
	if d == nil {
		return false
	}

	const macdBottomBuffer = 15.0

	reference :=
		continuationRefs.Reference(
			EntryProducerCase11BBottomReversal,
			SideBuy,
		)

	macdPreBottomThreshold :=
		-macd.EPS + macdBottomBuffer
	macdPreBottomZone :=
		macd.LinePrev6 <= macdPreBottomThreshold

	continuation := reference > 0
	nextEntryPrice := 0.0
	entryGatePass := pyramid.Buy.GatePassed

	if continuation {
		nextEntryPrice, entryGatePass =
			continuationReferenceGate(
				SideBuy,
				price,
				reference,
			)
	}

	confidence := ai.Confidence
	confidenceFallback := false
	if confidence <= 0 {
		confidence = 1.0
		confidenceFallback = true
	}

	bottomReversalBuy :=
		macdPreBottomZone &&
			ema.LowBottom &&
			entryGatePass

	d.MACDPreBottomZone = macdPreBottomZone
	d.BottomReversalBuy = bottomReversalBuy

	if !bottomReversalBuy {
		return false
	}

	d.Signal = Buy
	d.Confidence = confidence
	d.PyramidPass = pyramid.Buy.GatePassed
	d.PyramidReason = pyramid.Buy.Reason
	d.Producer = EntryProducerCase11BBottomReversal
	d.PendingCancelPolicy = PendingSignalCancelDisabled

	applyStandardProducerEconomics(
		d,
		EntryProducerCase11BBottomReversal,
		continuation,
		reference,
		nextEntryPrice,
		entryGatePass,
	)

	d.ProducerReason = fmt.Sprintf(
		"bottom_reversal_buy|"+
			"ai_raw=%s|ai_confidence=%.6f|case11b_confidence=%.6f|confidence_fallback=%t|"+
			"macd_idx6=%.6f|eps=%.6f|buffer=%.2f|threshold=%.6f|"+
			"macd_zone=%t|ema_low_bottom=%t|"+
			"reference_mode=%s|reference_price=%.8f|next_entry_price=%.8f|"+
			"continuation_spacing_pct=%.4f|entry_gate_pass=%t|pyramid_buy=%t|"+
			"tier=%s|tier_mult=%.6f|continuation=%t|"+
			"continuation_profit_factor=%.6f|profit_gate_mult=%.6f",
		ai.Raw,
		ai.Confidence,
		confidence,
		confidenceFallback,
		macd.LinePrev6,
		macd.EPS,
		macdBottomBuffer,
		macdPreBottomThreshold,
		macdPreBottomZone,
		ema.LowBottom,
		map[bool]string{true: "continuation_reference", false: "first_pyramid"}[continuation],
		reference,
		nextEntryPrice,
		ContinuationEntrySpacingPct,
		entryGatePass,
		pyramid.Buy.GatePassed,
		d.ProducerTier,
		d.ProducerTierMultiplier,
		d.IsContinuation,
		ContinuationProfitGateFactor,
		d.ProfitGateMultiplier,
	)

	return true
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
	continuationRefs ProducerContinuationReferences,
) bool {
	const (
		minConfidence  = 0.65
		maxNearPeakPct = 0.10
	)

	case13AReferencePrice :=
		continuationRefs.Reference(
			EntryProducerCase13APeakSell,
			SideSell,
		)

	case13APending :=
		pendingCounts.Count(
			EntryProducerCase13APeakSell,
			SideSell,
		)

	// Pending count is the simultaneous-duplicate guard.
	case13AAvailable :=
		case13APending == 0

	case13AContinuation :=
		case13AReferencePrice > 0

	nextCase13AReentryPrice := 0.0
	case13AReentryPass :=
		pyramid.Sell.SpacingPass

	if case13AContinuation {
		nextCase13AReentryPrice, case13AReentryPass =
			continuationReferenceGate(
				SideSell,
				price,
				case13AReferencePrice,
			)
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
	d.Producer = EntryProducerCase13APeakSell
	d.PendingCancelPolicy = PendingSignalCancelDisabled

	applyStandardProducerEconomics(
		d,
		EntryProducerCase13APeakSell,
		case13AContinuation,
		case13AReferencePrice,
		nextCase13AReentryPrice,
		case13AReentryPass,
	)

	referenceMode := "continuation_reference"
	if !case13AContinuation {
		referenceMode = "first_spacing"
	}

	d.ProducerReason = fmt.Sprintf(
		"peak_sell|"+
			"confidence=%.2f|regime=%s|"+
			"near_peak_pct=%.6f|"+
			"macd_idx6=%.6f|macd_line=%.6f|macd_hist=%.6f|"+
			"ema_high_peak=%t|pending=%d|"+
			"reference_mode=%s|reference_price=%.8f|next_entry_price=%.8f|"+
			"continuation_spacing_pct=%.4f|spacing=%t|entry_gate_pass=%t|"+
			"tier=%s|tier_mult=%.6f|continuation=%t|"+
			"continuation_profit_factor=%.6f|profit_gate_mult=%.6f",
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
		ContinuationEntrySpacingPct,
		pyramid.Sell.SpacingPass,
		case13AReentryPass,
		d.ProducerTier,
		d.ProducerTierMultiplier,
		d.IsContinuation,
		ContinuationProfitGateFactor,
		d.ProfitGateMultiplier,
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
	continuationRefs ProducerContinuationReferences,
) bool {
	const (
		minConfidence = 0.65
		maxNearLowPct = 0.10
	)

	case13BReferencePrice :=
		continuationRefs.Reference(
			EntryProducerCase13BBottomBuy,
			SideBuy,
		)
	case13BContinuation :=
		case13BReferencePrice > 0

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

	case13BEntryGatePass :=
		pyramid.Buy.SpacingPass &&
			case13BAdversePass
	nextCase13BEntryPrice := 0.0

	if case13BContinuation {
		nextCase13BEntryPrice, case13BEntryGatePass =
			continuationReferenceGate(
				SideBuy,
				price,
				case13BReferencePrice,
			)
	}

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
			case13BEntryGatePass &&
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

	applyStandardProducerEconomics(
		d,
		EntryProducerCase13BBottomBuy,
		case13BContinuation,
		case13BReferencePrice,
		nextCase13BEntryPrice,
		case13BEntryGatePass,
	)

	d.ProducerReason = fmt.Sprintf(
		"bottom_buy|"+
			"confidence=%.2f|regime=%s|"+
			"price=%.8f|recent_low=%.8f|near_low_pct=%.6f|"+
			"macd_idx6=%.6f|macd_line=%.6f|macd_hist=%.6f|"+
			"ema_low_bottom=%t|spacing=%t|"+
			"pending=%d|adverse_required=%t|buy_latched=%.8f|"+
			"adverse_reached=%t|adverse_pass=%t|"+
			"reference_price=%.8f|next_entry_price=%.8f|"+
			"continuation_spacing_pct=%.4f|entry_gate_pass=%t|"+
			"tier=%s|tier_mult=%.6f|continuation=%t|"+
			"continuation_profit_factor=%.6f|profit_gate_mult=%.6f",
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
		case13BReferencePrice,
		nextCase13BEntryPrice,
		ContinuationEntrySpacingPct,
		case13BEntryGatePass,
		d.ProducerTier,
		d.ProducerTierMultiplier,
		d.IsContinuation,
		ContinuationProfitGateFactor,
		d.ProfitGateMultiplier,
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
// First entry requires the Case14B native buffered-latch / BUY-spacing
// admission. Continuation preserves AI/Legacy/Logic/pattern/regime qualification
// but replaces that native price admission with the standardized -0.20%
// committed-reference gate.
func applyCase14BUptrendBuyProducer(
	d *EntryDecision,
	ai AIResult,
	macd MACDResult,
	ema EMAPatternResult,
	pyramid PyramidResult,
	price float64,
	regime MarketRegime,
	pendingCounts PendingProducerCounts,
	continuationRefs ProducerContinuationReferences,
) bool {
	legacy :=
		evaluateLegacyDirection(
			ai,
			macd,
			ema,
		)

	const (
		minConfidence      = 0.30
		nearLatchBufferPct = 0.56
	)

	case14BReferencePrice :=
		continuationRefs.Reference(
			EntryProducerCase14BUptrendBuy,
			SideBuy,
		)
	case14BContinuation :=
		case14BReferencePrice > 0

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

	withinLatchWindow :=
		latchValid &&
			!actualLatchReached &&
			price <= bufferedLatch

	entryGatePass :=
		withinLatchWindow &&
			pyramid.Buy.SpacingPass
	nextCase14BEntryPrice := 0.0

	if case14BContinuation {
		nextCase14BEntryPrice, entryGatePass =
			continuationReferenceGate(
				SideBuy,
				price,
				case14BReferencePrice,
			)
	}

	// Pending single-flight protection remains authoritative in both first and
	// continuation modes. Only the native Pyramid/latch price admission is
	// replaced by the standardized continuation reference gate.
	case14BAvailable :=
		case14BPending == 0 &&
			entryGatePass

	uptrendBuy :=
		case14BAvailable &&
			ai.Raw == Buy &&
			ai.Confidence >= minConfidence &&
			legacy.Signal == Buy &&
			legacy.LogicOpinion == Buy &&
			regime == RegimeUp &&
			ema.PatternBuy

	if !uptrendBuy {
		return false
	}

	d.Signal = Buy
	d.PyramidReason = pyramid.Buy.Reason
	d.Producer = EntryProducerCase14BUptrendBuy
	d.PendingCancelPolicy = PendingSignalCancelDisabled

	applyStandardProducerEconomics(
		d,
		EntryProducerCase14BUptrendBuy,
		case14BContinuation,
		case14BReferencePrice,
		nextCase14BEntryPrice,
		entryGatePass,
	)

	d.ProducerReason = fmt.Sprintf(
		"uptrend_buffered_latch_buy|"+
			"confidence=%.2f|regime=%s|price=%.8f|"+
			"latch=%.8f|buffered_latch=%.8f|actual_latch=%t|"+
			"within_window=%t|spacing=%t|pending=%d|"+
			"legacy=%s|logic=%s|pattern_buy=%t|"+
			"reference_price=%.8f|next_entry_price=%.8f|"+
			"continuation_spacing_pct=%.4f|entry_gate_pass=%t|"+
			"tier=%s|tier_mult=%.6f|continuation=%t|"+
			"continuation_profit_factor=%.6f|profit_gate_mult=%.6f",
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
		case14BReferencePrice,
		nextCase14BEntryPrice,
		ContinuationEntrySpacingPct,
		entryGatePass,
		d.ProducerTier,
		d.ProducerTierMultiplier,
		d.IsContinuation,
		ContinuationProfitGateFactor,
		d.ProfitGateMultiplier,
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

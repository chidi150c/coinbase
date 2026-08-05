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
	"log"
)

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
	equity EquityResult,
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

	// Case 11A has priority over Case 11B if both somehow evaluate true.
	if peakReversalSell {
		d.Signal = Sell
		d.Producer =
			EntryProducerCase11APeakReversal

		d.PyramidPass =
			pyramid.Sell.GatePassed
		d.PyramidReason =
			pyramid.Sell.Reason

		// Diagnostic only. Equity does not gate Case 11A.
		d.EquityPass =
			equity.SellTrigger
		d.EquityReason =
			equity.Reason

		log.Printf(
			"[TRACE] case11A.peak_reversal_sell "+
				"macd_idx6=%.6f eps=%.6f buffer=%.2f threshold=%.6f "+
				"macd_zone=%t ema_high_peak=%t "+
				"pyramid_sell=%t pyramid_reason=%s "+
				"equity_sell=%t equity_reason=%s",
			macd.LinePrev6,
			macd.EPS,
			macdPeakBuffer,
			macdPrePeakThreshold,
			macdPrePeakZone,
			ema.HighPeak,
			pyramid.Sell.GatePassed,
			pyramid.Sell.Reason,
			equity.SellTrigger,
			equity.Reason,
		)

		return true
	}

	if bottomReversalBuy {
		d.Signal = Buy
		d.Producer =
			EntryProducerCase11BBottomReversal

		d.PyramidPass =
			pyramid.Buy.GatePassed
		d.PyramidReason =
			pyramid.Buy.Reason

		// Diagnostic only. Equity does not gate Case 11B.
		d.EquityPass =
			equity.BuyTrigger
		d.EquityReason =
			equity.Reason

		log.Printf(
			"[TRACE] case11B.bottom_reversal_buy "+
				"macd_idx6=%.6f eps=%.6f buffer=%.2f threshold=%.6f "+
				"macd_zone=%t ema_low_bottom=%t "+
				"pyramid_buy=%t pyramid_reason=%s "+
				"equity_buy=%t equity_reason=%s",
			macd.LinePrev6,
			macd.EPS,
			macdBottomBuffer,
			macdPreBottomThreshold,
			macdPreBottomZone,
			ema.LowBottom,
			pyramid.Buy.GatePassed,
			pyramid.Buy.Reason,
			equity.BuyTrigger,
			equity.Reason,
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
	equity EquityResult,
	price float64,
	recentLow float64,
	recentHigh float64,
	regime MarketRegime,
	pendingCounts PendingProducerCounts,
) bool {
	if applyCase13APeakProducer(
		d,
		ai,
		macd,
		ema,
		pyramid,
		equity,
		price,
		recentHigh,
		regime,
		pendingCounts,
	) {
		return true
	}

	if applyCase13BBottomProducer(
		d,
		ai,
		macd,
		ema,
		pyramid,
		equity,
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
	equity EquityResult,
	price float64,
	recentHigh float64,
	regime MarketRegime,
	pendingCounts PendingProducerCounts,
) bool {
	const (
		minConfidence  = 0.65
		maxNearPeakPct = 0.10
	)

	// Case 12 extension for Case 13A:
	//
	// The first Case13A SELL may qualify without the adverse latch.
	// While another Case13A SELL remains pending, a subsequent Case13A
	// SELL must reach the advanced SELL latch.
	case13APending :=
		pendingCounts.Count(
			EntryProducerCase13APeakSell,
			SideSell,
		)

	case13AAdverseRequired :=
		case13APending > 0

	sellAdverseReached :=
		pyramid.Sell.Latched > 0 &&
			price >= pyramid.Sell.Latched

	case13AAdversePass :=
		!case13AAdverseRequired ||
			sellAdverseReached

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

	// The arm identifies the complete peak environment.
	//
	// Case13A normally requires only Pyramid SELL spacing. However, when
	// another Case13A SELL is already pending, it additionally requires
	// price to reach the SELL latch advanced during pending registration.
	peakSellArm :=
		ai.Raw == Sell &&
			ai.Confidence >= minConfidence &&
			regime == RegimeUp &&
			pyramid.Sell.SpacingPass &&
			case13AAdversePass &&
			priceNearRecentHigh &&
			macd.LinePrev6 > 0 &&
			macd.Line > 0 &&
			macd.Hist > 0

	// EMA high-peak geometry is the final structural confirmation.
	peakSell :=
		peakSellArm &&
			ema.HighPeak

	log.Printf(
		"[TRACE] case13A.peak_sell.evaluate "+
			"ai_raw=%s confidence=%.2f min_confidence=%.2f regime=%s "+
			"price=%.8f recent_peak=%.8f near_peak_pct=%.6f "+
			"price_near_peak=%t "+
			"macd_idx6=%.6f macd_line=%.6f macd_hist=%.6f "+
			"ema_high_peak=%t "+
			"pyramid_spacing=%t "+
			"pending_count=%d adverse_required=%t "+
			"sell_latched=%.8f adverse_reached=%t adverse_pass=%t "+
			"arm=%t producer=%t",
		ai.Raw,
		ai.Confidence,
		minConfidence,
		regime,
		price,
		recentHigh,
		nearPeakPct,
		priceNearRecentHigh,
		macd.LinePrev6,
		macd.Line,
		macd.Hist,
		ema.HighPeak,
		pyramid.Sell.SpacingPass,
		case13APending,
		case13AAdverseRequired,
		pyramid.Sell.Latched,
		sellAdverseReached,
		case13AAdversePass,
		peakSellArm,
		peakSell,
	)

	if !peakSell {
		return false
	}

	d.Signal = Sell
	d.Producer = EntryProducerCase13APeakSell

	// Case13A requires SELL spacing. The complete ordinary Pyramid gate
	// is not required for the first entry. Its advanced latch is required
	// only when another Case13A SELL is already pending.
	d.PyramidPass =
		pyramid.Sell.SpacingPass &&
			case13AAdversePass

	d.PyramidReason = pyramid.Sell.Reason

	// Equity is recorded for diagnostics only.
	d.EquityPass = equity.SellTrigger
	d.EquityReason = equity.Reason

	log.Printf(
		"[TRACE] case13A.peak_sell.produced "+
			"producer=%s side=%s price=%.8f "+
			"pending_count=%d adverse_required=%t "+
			"sell_latched=%.8f adverse_reached=%t",
		EntryProducerCase13APeakSell,
		SideSell,
		price,
		case13APending,
		case13AAdverseRequired,
		pyramid.Sell.Latched,
		sellAdverseReached,
	)

	return true
}

func applyCase13BBottomProducer(
	d *EntryDecision,
	ai AIResult,
	macd MACDResult,
	ema EMAPatternResult,
	pyramid PyramidResult,
	equity EquityResult,
	price float64,
	recentLow float64,
	regime MarketRegime,
	pendingCounts PendingProducerCounts,
) bool {
	const (
		minConfidence = 0.65
		maxNearLowPct = 0.10
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

	log.Printf(
		"[TRACE] case13B.bottom_buy.evaluate "+
			"ai_raw=%s confidence=%.2f min_confidence=%.2f regime=%s "+
			"price=%.8f recent_low=%.8f near_low_pct=%.6f "+
			"max_near_low_pct=%.2f price_near_low=%t "+
			"macd_idx6=%.6f macd_line=%.6f macd_hist=%.6f "+
			"ema_low_bottom=%t "+
			"pyramid_buy_spacing=%t "+
			"pending_count=%d adverse_required=%t "+
			"buy_latched=%.8f adverse_reached=%t adverse_pass=%t "+
			"arm=%t producer=%t "+
			"equity_buy=%t equity_reason=%s",
		ai.Raw,
		ai.Confidence,
		minConfidence,
		regime,
		price,
		recentLow,
		nearLowPct,
		maxNearLowPct,
		priceNearRecentLow,
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
		bottomBuyArm,
		bottomBuy,
		equity.BuyTrigger,
		equity.Reason,
	)

	if !bottomBuy {
		return false
	}

	d.Signal = Buy
	d.Producer = EntryProducerCase13BBottomBuy

	// Case13B requires BUY spacing. The complete ordinary Pyramid gate
	// is not required for the first entry. Its advanced latch is required
	// only when another Case13B BUY is already pending.
	d.PyramidPass =
		pyramid.Buy.SpacingPass &&
			case13BAdversePass

	d.PyramidReason = pyramid.Buy.Reason

	// Equity is recorded for diagnostics only.
	d.EquityPass = equity.BuyTrigger
	d.EquityReason = equity.Reason

	log.Printf(
		"[TRACE] case13B.bottom_buy.produced "+
			"producer=%s side=%s price=%.8f "+
			"pending_count=%d adverse_required=%t "+
			"buy_latched=%.8f adverse_reached=%t",
		EntryProducerCase13BBottomBuy,
		SideBuy,
		price,
		case13BPending,
		case13BAdverseRequired,
		pyramid.Buy.Latched,
		buyAdverseReached,
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
	ema EMAPatternResult,
	pyramid PyramidResult,
	equity EquityResult,
	price float64,
	regime MarketRegime,
	legacySignal Signal,
	logicOpinion Signal,
	pendingCounts PendingProducerCounts,
) bool {
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
			legacySignal == Buy &&
			logicOpinion == Buy &&
			regime == RegimeUp &&
			ema.PatternBuy &&
			pyramid.Buy.SpacingPass

	log.Printf(
		"[TRACE] case14B.uptrend_buy.evaluate "+
			"ai_raw=%s confidence=%.2f min_confidence=%.2f "+
			"legacy=%s logic=%s regime=%s pattern_buy=%t "+
			"price=%.8f latch=%.8f buffered_latch=%.8f "+
			"buffer_pct=%.4f actual_latch_reached=%t "+
			"within_latch_window=%t spacing=%t "+
			"pending_count=%d available=%t "+
			"profit_gate_mult=%.2f producer=%t",
		ai.Raw,
		ai.Confidence,
		minConfidence,
		legacySignal,
		logicOpinion,
		regime,
		ema.PatternBuy,
		price,
		pyramid.Buy.Latched,
		bufferedLatch,
		nearLatchBufferPct,
		actualLatchReached,
		withinLatchWindow,
		pyramid.Buy.SpacingPass,
		case14BPending,
		case14BAvailable,
		profitGateMultiplier,
		uptrendBuy,
	)

	if !uptrendBuy {
		return false
	}

	d.Signal = Buy
	d.Producer = EntryProducerCase14BUptrendBuy
	d.ProfitGateMultiplier = profitGateMultiplier

	d.PyramidReason = pyramid.Buy.Reason

	d.EquityPass = equity.BuyTrigger
	d.EquityReason = equity.Reason

	return true
}

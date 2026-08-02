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
//   - Assign a unique EntryDecisionSource for traceability.
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
//	4. Define its own diagnostics and DecisionSource.
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
		d.DecisionSource =
			EntryDecisionSourcePeakReversal

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
		d.DecisionSource =
			EntryDecisionSourceBottomReversal

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
) bool {
	const (
		minConfidence  = 0.65
		maxNearPeakPct = 0.10
	)

	nearPeakPct := 0.0
	PriceNearRecentHigh := false

	if recentHigh > 0 && price > 0 {
		nearPeakPct =
			(recentHigh - price) /
				recentHigh * 100.0

		PriceNearRecentHigh = nearPeakPct <= maxNearPeakPct
	}

	// The arm identifies the complete peak environment. It does not
	// produce SELL until EMA supplies the high-peak trigger.
	peakSellArm :=
		ai.Raw == Sell &&
			ai.Confidence >= minConfidence &&
			regime == RegimeUp &&
			pyramid.Sell.SpacingPass &&
			PriceNearRecentHigh &&
			macd.LinePrev6 > 0 &&
			macd.Line > 0 &&
			macd.Hist > 0

		// EMA high-peak geometry is the final structural confirmation.
	peakSell :=
		peakSellArm &&
			ema.HighPeak

	// Preserve Case 13A interpretation regardless of whether the
	// producer emits SELL.
	d.NearRecentHighPct = nearPeakPct
	d.PriceNearRecentHigh = PriceNearRecentHigh

	if !peakSell {
		return false
	}

	d.Signal = Sell
	d.DecisionSource = EntryDecisionSourceCase13APeakSell

	// Case 13A requires SELL spacing, not the normal Pyramid gate.
	d.PyramidPass = pyramid.Sell.SpacingPass
	d.PyramidReason = pyramid.Sell.Reason

	// Equity is recorded for diagnostics only.
	d.EquityPass = equity.SellTrigger
	d.EquityReason = equity.Reason

	log.Printf(
		"[TRACE] case13A.peak_sell "+
			"ai_raw=%s confidence=%.2f regime=%s "+
			"price=%.8f recent_peak=%.8f near_peak_pct=%.6f "+
			"macd_idx6=%.6f macd_line=%.6f macd_hist=%.6f "+
			"ema_high_peak=%t",
		ai.Raw,
		ai.Confidence,
		regime,
		price,
		recentHigh,
		nearPeakPct,
		macd.LinePrev6,
		macd.Line,
		macd.Hist,
		ema.HighPeak,
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
) bool {

	const (
		minConfidence = 0.65
		maxNearLowPct = 0.10
	)

	nearLowPct := 0.0
	priceNearRecentLow := false

	if recentLow > 0 && price > 0 {
		nearLowPct = (price - recentLow) / recentLow * 100.0
		priceNearRecentLow = nearLowPct <= maxNearLowPct
	}

	// The arm identifies the complete bottom environment. It does not
	// produce BUY until EMA supplies the low-bottom trigger.
	bottomBuyArm :=
		ai.Raw == Buy &&
			ai.Confidence >= minConfidence &&
			regime == RegimeDown &&
			pyramid.Buy.SpacingPass &&
			priceNearRecentLow &&
			macd.LinePrev6 < 0 &&
			macd.Line < 0 &&
			macd.Hist < 0

		// EMA low-bottom geometry is the final structural confirmation.
	bottomBuy :=
		bottomBuyArm &&
			ema.LowBottom

	// Preserve Case 13 interpretation in the canonical decision record
	// regardless of whether the producer emits BUY.
	d.NearRecentLowPct = nearLowPct
	d.PriceNearRecentLow = priceNearRecentLow

	if !bottomBuy {
		return false
	}

	d.Signal = Buy
	d.DecisionSource = EntryDecisionSourceCase13BBottomBuy

	// Case 13 requires BUY spacing, not the normal Pyramid adverse gate.
	d.PyramidPass = pyramid.Buy.SpacingPass
	d.PyramidReason = pyramid.Buy.Reason

	// Equity is recorded for diagnostics only. It does not gate Case 13.
	d.EquityPass = equity.BuyTrigger
	d.EquityReason = equity.Reason

	log.Printf(
		"[TRACE] case13B.bottom_buy "+
			"ai_raw=%s confidence=%.2f min_confidence=%.2f regime=%s "+
			"price=%.8f recent_low=%.8f near_low_pct=%.6f max_near_low_pct=%.2f price_near_low=%t "+
			"macd_idx6=%.6f macd_line=%.6f macd_hist=%.6f "+
			"pyramid_buy_spacing=%t pyramid_buy_gate=%t pyramid_reason=%s "+
			"ema_low_bottom=%t arm=%t producer=%t "+
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
		pyramid.Buy.SpacingPass,
		pyramid.Buy.GatePassed,
		pyramid.Buy.Reason,
		ema.LowBottom,
		bottomBuyArm,
		bottomBuy,
		equity.BuyTrigger,
		equity.Reason,
	)

	return true
}

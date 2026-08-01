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

import "log"

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

// applycase13BCapitulationBottomProducer evaluates Case 13, an independent
// Capitulation-Bottom BUY producer.
//
// Case 13 targets an early reversal from a persistent bearish condition:
//
//   - AI has produced BUY with at least 0.65 confidence.
//   - Market regime remains DOWN.
//   - BUY-side spacing protection has passed.
//   - Live price is no more than 0.10% above the recent low.
//   - MACD was negative at idx-6 and remains negative now.
//   - MACD histogram remains negative.
//   - EMA low-bottom geometry provides the final entry trigger.
//
// The normal Pyramid BUY gate is intentionally not required. Case 13 uses
// Pyramid spacing for entry-frequency protection while proximity to the
// recent low and EMA low-bottom geometry qualify the entry location.
//
// The function always populates the Case 13 diagnostic fields in d. It
// returns true only when Case 13 emits an independent BUY decision.
func applycase13BCapitulationBottomProducer(
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
		priceNearRecentLow = nearLowPct >= 0 && nearLowPct <= maxNearLowPct
	}

	// The arm identifies the complete capitulation environment. It does
	// not produce BUY until EMA supplies the low-bottom trigger.
	capitulationBuyArm :=
		ai.Raw == Buy &&
			ai.Confidence >= minConfidence &&
			regime == RegimeDown &&
			pyramid.Buy.SpacingPass &&
			priceNearRecentLow &&
			macd.LinePrev6 < 0 &&
			macd.Line < 0 &&
			macd.Hist < 0

	// EMA low-bottom geometry is the final structural confirmation.
	capitulationBottomBuy :=
		capitulationBuyArm &&
			ema.LowBottom

	// Preserve Case 13 interpretation in the canonical decision record
	// regardless of whether the producer emits BUY.
	d.NearRecentLowPct = nearLowPct
	d.PriceNearRecentLow = priceNearRecentLow
	d.CapitulationBuyArm = capitulationBuyArm
	d.CapitulationBottomBuy = capitulationBottomBuy

	if !capitulationBottomBuy {
		return false
	}

	d.Signal = Buy
	d.DecisionSource = EntryDecisionSourceCapitulationBottomBuy

	// Case 13 requires BUY spacing, not the normal Pyramid adverse gate.
	d.PyramidPass = pyramid.Buy.SpacingPass
	d.PyramidReason = pyramid.Buy.Reason

	// Equity is recorded for diagnostics only. It does not gate Case 13.
	d.EquityPass = equity.BuyTrigger
	d.EquityReason = equity.Reason

	log.Printf(
		"[TRACE] case13B.capitulation_bottom_buy "+
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
		capitulationBuyArm,
		capitulationBottomBuy,
		equity.BuyTrigger,
		equity.Reason,
	)

	return true
}

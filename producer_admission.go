package main

import (
	"fmt"
	"time"
)

// ProducerAdmissionResult is the deterministic outcome of non-resource entry
// admission for one already-qualified producer decision.
//
// Admission answers only:
//
//	"Is this producer allowed to proceed to sizing/resource coordination?"
//
// It does not size the trade, inspect available funding, allocate resources,
// register pending entries, or submit orders.
type ProducerAdmissionResult struct {
	Allowed   bool
	ErrorCode EntryProduceErrorCode
	Err       error
	Message   string
}

func producerAdmissionAllowed() ProducerAdmissionResult {
	return ProducerAdmissionResult{
		Allowed: true,
	}
}

func producerAdmissionBlocked(
	code EntryProduceErrorCode,
	err error,
	message string,
) ProducerAdmissionResult {
	return ProducerAdmissionResult{
		Allowed:   false,
		ErrorCode: code,
		Err:       err,
		Message:   message,
	}
}

// evaluateProducerAdmissionLocked applies non-resource entry protections to one
// producer decision.
//
// Caller must hold t.mu because Case3B reads t.lastExits and the current regime.
//
// V1 admission policy preserved from the old inline step() path:
//
//   - Case3B DOWN-regime BUY protection:
//     non-LOW producers may not BUY above the latest BUY threshold-stop-loss
//     exit price from the preceding 24 hours.
//
//   - Case3B UP-regime SELL protection:
//     non-LOW producers may not SELL below the latest SELL threshold-stop-loss
//     exit price from the preceding 24 hours.
//
//   - Case3AReplacement explicitly bypasses Case3B.
//
//   - LongOnly blocks SELL.
//
// A blocked producer owns its own DecisionID/lifecycle event.  In the parallel
// producer flow the caller records that event and continues with the remaining
// producer decisions rather than terminating the whole producer batch.
func (t *Trader) evaluateProducerAdmissionLocked(
	d EntryDecision,
	price float64,
	wallNow time.Time,
) ProducerAdmissionResult {
	side, ok := d.SignalToSide()
	if !ok {
		return producerAdmissionBlocked(
			EntryProduceErrInvalidSide,
			fmt.Errorf(
				"decision signal cannot map to order side: %v",
				d.Signal,
			),
			"HOLD invalid producer side",
		)
	}

	// Preserve the existing Case3B asymmetry exactly: only SELL explicitly
	// exempts Case3AReplacement. Do not silently invent a BUY exemption.
	case3AReplacement :=
		d.Producer == EntryProducerCase3AReplacement

	// -------------------------------------------------------------------------
	// Case 3B-Opposite — DOWN-Regime BUY Protection
	// -------------------------------------------------------------------------
	if side == SideBuy &&
		d.ProducerTier != ProducerTierLow &&
		t.MarketRegime == RegimeDown {

		lastLossExit, found :=
			latestThresholdStopLossExitWithin(
				t.lastExits,
				SideBuy,
				wallNow,
				24*time.Hour,
			)

		if found &&
			price > lastLossExit.ClosePrice {

			return producerAdmissionBlocked(
				EntryProduceErrDecisionCase3BBlocked,
				fmt.Errorf(
					"Case3B BUY blocked above latest threshold-stop loss exit price %.8f",
					lastLossExit.ClosePrice,
				),
				"HOLD Case3B block BUY above latest loss-exit SELL price",
			)
		}
	}

	// -------------------------------------------------------------------------
	// Case 3B — UP-Regime SELL Protection
	// -------------------------------------------------------------------------
	if side == SideSell &&
		!case3AReplacement &&
		d.ProducerTier != ProducerTierLow &&
		t.MarketRegime == RegimeUp {

		lastLossExit, found :=
			latestThresholdStopLossExitWithin(
				t.lastExits,
				SideSell,
				wallNow,
				24*time.Hour,
			)

		if found &&
			price < lastLossExit.ClosePrice {

			return producerAdmissionBlocked(
				EntryProduceErrDecisionCase3BBlocked,
				fmt.Errorf(
					"Case3B SELL blocked below latest threshold-stop loss exit price %.8f",
					lastLossExit.ClosePrice,
				),
				"HOLD Case3B block SELL below latest loss-exit BUY price",
			)
		}
	}

	// LongOnly is a producer admission policy, not a resource-allocation rule.
	if side == SideSell &&
		t.cfg.LongOnly {

		return producerAdmissionBlocked(
			EntryProduceErrDecisionLongOnlyBlocked,
			fmt.Errorf(
				"SELL blocked because LongOnly is enabled",
			),
			fmt.Sprintf(
				"FLAT (long-only) [%s]",
				decisionEntryReason(d),
			),
		)
	}

	return producerAdmissionAllowed()
}

package main

import "time"

const (
	aiTransitionHoldPeriod = 100 * time.Minute
	aiTransitionNetTargetUSD = 1.0
	aiTransitionGivebackUSD = 0.20
)

// aiTransitionHoldStatus updates the durable peak for a single filled lot.
// Profit is net of its entry fee and an estimated exit fee at the current mark.
func aiTransitionHoldStatus(lot *Position, now time.Time, net float64) (expired, unlocked, giveback bool) {
	if lot == nil {
		return false, false, false
	}
	if !lot.OpenTime.IsZero() && !now.Before(lot.OpenTime) {
		expired = now.Sub(lot.OpenTime) >= aiTransitionHoldPeriod
	}
	if net >= aiTransitionNetTargetUSD && !lot.AITransitionTargetArmed {
		lot.AITransitionTargetArmed = true
		lot.AITransitionPeakNetUSD = net
	}
	if lot.AITransitionTargetArmed && net > lot.AITransitionPeakNetUSD {
		lot.AITransitionPeakNetUSD = net
	}
	giveback = lot.AITransitionTargetArmed && net <= lot.AITransitionPeakNetUSD-aiTransitionGivebackUSD
	return expired, expired || lot.AITransitionTargetArmed, giveback
}

// The loss guard applies to the destination side, not to the current lot name.
func aiTransitionLosingRolloverAllowed(side OrderSide, regime MarketRegime, net float64) bool {
	if net >= 0 {
		return true
	}
	if regime == RegimeUp && side == SideBuy { // would sell against UP
		return false
	}
	if regime == RegimeDown && side == SideSell { // would buy against DOWN
		return false
	}
	return true
}

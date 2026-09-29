package main

import (
	"testing"
	"time"
)

func TestAITransitionHoldingClockAndNetGiveback(t *testing.T) {
	start := time.Date(2026, 9, 29, 12, 0, 0, 0, time.UTC)
	lot := &Position{OpenTime: start}
	if expired, unlocked, giveback := aiTransitionHoldStatus(lot, start.Add(99*time.Minute), 0.99); expired || unlocked || giveback {
		t.Fatal("lot unlocked before either condition")
	}
	if expired, unlocked, giveback := aiTransitionHoldStatus(lot, start.Add(99*time.Minute), 1.01); expired || !unlocked || giveback {
		t.Fatal("net target did not unlock the lot")
	}
	aiTransitionHoldStatus(lot, start.Add(99*time.Minute), 1.40)
	if _, _, giveback := aiTransitionHoldStatus(lot, start.Add(99*time.Minute), 1.21); giveback {
		t.Fatal("trail fired before 0.20 USDT giveback")
	}
	if _, _, giveback := aiTransitionHoldStatus(lot, start.Add(99*time.Minute), 1.19); !giveback {
		t.Fatal("trail did not fire from the peak")
	}
	other := &Position{OpenTime: start}
	if expired, unlocked, giveback := aiTransitionHoldStatus(other, start.Add(100*time.Minute), -1.3); !expired || !unlocked || giveback {
		t.Fatal("100-minute expiry did not unlock a losing lot")
	}
}

func TestAITransitionLosingRolloverFollowsDestinationRegime(t *testing.T) {
	for _, tc := range []struct {
		side   OrderSide
		regime MarketRegime
		want   bool
	}{
		{SideSell, RegimeUp, true},
		{SideBuy, RegimeUp, false},
		{SideBuy, RegimeDown, true},
		{SideSell, RegimeDown, false},
		{SideSell, RegimeNormal, true}, // NORMAL has a separate profit-price guard.
	} {
		if got := aiTransitionLosingRolloverAllowed(tc.side, tc.regime, -0.1); got != tc.want {
			t.Errorf("side=%s regime=%s got=%t want=%t", tc.side, tc.regime, got, tc.want)
		}
	}
}

package main

import (
	"context"
	"math"
	"testing"
	"time"
)

func countedReservation(id string, side OrderSide, quote, base float64, kind ResourceReservationKind) ResourceReservation {
	return ResourceReservation{
		ID: id, OwnerID: id, Kind: kind, State: ResourceReservationReserved,
		Side: side, QuoteUSD: quote, Base: base, CreatedAt: time.Now().UTC(),
	}
}

func TestCurrentSpareQuoteUsesAllResourceManagerOwnership(t *testing.T) {
	broker := &resetTestBroker{price: 100, quote: 200, base: 2, open: map[string]bool{}}
	trader := newResetTestTrader(broker)
	trader.resourceManager = NewResourceManager(ResourceLedgerState{})

	reservations := []ResourceReservation{
		countedReservation("lot:sell-later", SideSell, 80, 0, ResourceReservationLot),
		countedReservation("pending-entry:buy", SideBuy, 20, 0, ResourceReservationPendingEntry),
		countedReservation("case3a:a", SideBuy, 10, 0, ResourceReservationCase3A),
		countedReservation("case3b:b", SideBuy, 5, 0, ResourceReservationCase3B),
		countedReservation("case3c:c", SideBuy, 4, 0, ResourceReservationCase3C),
		countedReservation("uncertain", SideBuy, 6, 0, ResourceReservationQuarantine),
	}
	if err := trader.resourceManager.ReserveBatch(reservations); err != nil {
		t.Fatal(err)
	}

	trader.mu.Lock()
	spare, err := trader.currentSpareQuoteLocked(context.Background())
	trader.mu.Unlock()
	if err != nil {
		t.Fatal(err)
	}
	if math.Abs(spare-75) > 1e-9 {
		t.Fatalf("spare quote=%.8f, want 75 after all ownership", spare)
	}
	if _, ok := trader.resourceManager.Reservation("lot:sell-later"); !ok {
		t.Fatal("source SELL lot reservation was released before confirmed exit")
	}
}

func TestCurrentSpareBaseUsesAllResourceManagerOwnership(t *testing.T) {
	broker := &resetTestBroker{price: 100, quote: 200, base: 2, open: map[string]bool{}}
	trader := newResetTestTrader(broker)
	trader.resourceManager = NewResourceManager(ResourceLedgerState{})
	if err := trader.resourceManager.ReserveBatch([]ResourceReservation{
		countedReservation("lot:buy", SideBuy, 0, 0.8, ResourceReservationLot),
		countedReservation("pending-entry:sell", SideSell, 0, 0.2, ResourceReservationPendingEntry),
		countedReservation("case3-base", SideSell, 0, 0.1, ResourceReservationCase3A),
		countedReservation("uncertain-base", SideSell, 0, 0.05, ResourceReservationQuarantine),
	}); err != nil {
		t.Fatal(err)
	}

	trader.mu.Lock()
	spare, _, err := trader.currentSpareBaseLocked(context.Background())
	trader.mu.Unlock()
	if err != nil {
		t.Fatal(err)
	}
	if math.Abs(spare-0.85) > 1e-9 {
		t.Fatalf("spare base=%.8f, want 0.85 after all ownership", spare)
	}
}

func TestFinalEntryProfitGateAppliesMultipliersBeforeMinimum(t *testing.T) {
	tests := []struct {
		name                         string
		configured, confidence, mult float64
		want                         float64
	}{
		{name: "LOW tier", configured: 0.70, confidence: 1, mult: LowTierProducerMultiplier, want: 0.35},
		{name: "reduced confidence", configured: 0.70, confidence: 0.20, mult: 1, want: 0.30},
		{name: "continuation", configured: 0.70, confidence: 1, mult: ContinuationProfitGateFactor, want: 0.56},
		{name: "combined reductions", configured: 0.70, confidence: 0.50, mult: LowTierProducerMultiplier, want: 0.30},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := finalEntryProfitGateUSD(tc.configured, tc.confidence, tc.mult, 0)
			if math.Abs(got-tc.want) > 1e-9 {
				t.Fatalf("gate=%.8f, want %.8f", got, tc.want)
			}
		})
	}
}

func TestFinalEntryProfitGateAddsRecoveryAfterMinimum(t *testing.T) {
	got := finalEntryProfitGateUSD(0.70, 0.20, LowTierProducerMultiplier, 0.25)
	if math.Abs(got-0.55) > 1e-9 {
		t.Fatalf("gate=%.8f, want ordinary minimum 0.30 plus recovery 0.25", got)
	}
}

func TestAITransitionRolloverInheritanceEnforcesFinalMinimum(t *testing.T) {
	if got := inheritedAITransitionProfitGateUSD(0.15); math.Abs(got-0.30) > 1e-9 {
		t.Fatalf("inherited gate=%.8f, want 0.30", got)
	}
	if got := inheritedAITransitionProfitGateUSD(0.70); math.Abs(got-0.70) > 1e-9 {
		t.Fatalf("inherited gate=%.8f, want 0.70", got)
	}
}

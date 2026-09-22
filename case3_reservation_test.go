package main

import (
	"math"
	"testing"
	"time"
)

func allocationForProducer(t *testing.T, plan AllocationPlan, producer EntryProducer) ProducerResourceAllocation {
	t.Helper()
	for _, allocation := range plan.Allocations {
		if allocation.Request.Producer == producer {
			return allocation
		}
	}
	t.Fatalf("allocation not found for producer %s", producer)
	return ProducerResourceAllocation{}
}

func quoteRequest(producer EntryProducer, priority ProducerPriority, amount float64) ProducerResourceRequest {
	return ProducerResourceRequest{
		Producer:          producer,
		Priority:          priority,
		Side:              SideBuy,
		RequestedQuote:    amount,
		RequestedBase:     amount / 100,
		RequestedResource: amount,
		CoreQuote:         amount,
		CoreBase:          amount / 100,
		ResourceKind:      ResourceKindQuote,
		MinimumResource:   10,
		ResourceStep:      0.01,
	}
}

func TestCase3WaitingReservationProtectsQuoteFromOrdinaryProducer(t *testing.T) {
	snapshot := ResourceSnapshot{
		AvailQuote: 100, ReservedQuote: 60, SpareQuote: 40,
		AvailBase: 1, SpareBase: 1,
		Price: 100, MinNotional: 10, AvailableLotSlots: -1,
	}
	ordinary := quoteRequest(EntryProducerNormalLegacy, ProducerPriority(100), 50)

	plan := (ProducerResourceCoordinator{}).Allocate(snapshot, []ProducerResourceRequest{ordinary}, true)
	allocation := allocationForProducer(t, plan, EntryProducerNormalLegacy)
	if allocation.Status != AllocationPartial || math.Abs(allocation.AllocatedQuote-40) > 1e-9 {
		t.Fatalf("ordinary allocation status=%s quote=%.8f, want partial 40", allocation.Status, allocation.AllocatedQuote)
	}
}

func TestExecutableCase3ReceivesOwnedReservationBeforeOrdinaryProducer(t *testing.T) {
	snapshot := ResourceSnapshot{
		AvailQuote: 100, ReservedQuote: 60, SpareQuote: 40,
		AvailBase: 1, SpareBase: 1,
		Price: 100, MinNotional: 10, AvailableLotSlots: -1,
	}
	case3 := quoteRequest(EntryProducerCase3AReplacement, ProducerPriority(800), 60)
	case3.ReservedOwnerID = "case3a:obligation-1"
	case3.OwnedReservedResource = 60
	ordinary := quoteRequest(EntryProducerNormalLegacy, ProducerPriority(100), 50)

	plan := (ProducerResourceCoordinator{}).Allocate(
		snapshot,
		[]ProducerResourceRequest{ordinary, case3},
		true,
	)
	case3Allocation := allocationForProducer(t, plan, EntryProducerCase3AReplacement)
	if case3Allocation.Status != AllocationApproved || math.Abs(case3Allocation.AllocatedQuote-60) > 1e-9 {
		t.Fatalf("Case3 allocation status=%s quote=%.8f, want approved 60", case3Allocation.Status, case3Allocation.AllocatedQuote)
	}
	ordinaryAllocation := allocationForProducer(t, plan, EntryProducerNormalLegacy)
	if ordinaryAllocation.Status != AllocationPartial || math.Abs(ordinaryAllocation.AllocatedQuote-40) > 1e-9 {
		t.Fatalf("ordinary allocation status=%s quote=%.8f, want partial 40", ordinaryAllocation.Status, ordinaryAllocation.AllocatedQuote)
	}
}

func TestCase3ReservationIsCountedUntilExchangeOrderOwnsIt(t *testing.T) {
	trader := &Trader{
		cfg: Config{FeeRatePct: 0.1, RequireBaseForShort: true},
		books: map[OrderSide]*SideBook{
			SideBuy:  {RunnerIDs: []int{}},
			SideSell: {RunnerIDs: []int{}},
		},
		pendingEntries:    make(map[string]*PendingEntry),
		pendingExits:      make(map[string]*PendingExit),
		RefundObligations: make(map[string]*RefundObligation),
		Case3AObligations: map[string]*Case3AObligation{
			"obligation-1": {
				ObligationID: "obligation-1", Side: SideBuy,
				TargetPrice: 100, RemainingBase: 0.5,
				Status:    Case3AObligationWaitingForTarget,
				CreatedAt: time.Now().UTC(),
			},
		},
		Case3BObligations: make(map[string]*Case3AObligation),
		resourceManager:   NewResourceManager(ResourceLedgerState{}),
	}
	if err := trader.rebuildDerivedResourceLedgerLocked(100); err != nil {
		t.Fatal(err)
	}
	reservation, ok := trader.resourceManager.Reservation("case3a:obligation-1")
	if !ok || reservation.Informational {
		t.Fatalf("waiting obligation reservation missing or informational: %+v", reservation)
	}
	if math.Abs(reservation.QuoteUSD-50.05) > 1e-9 {
		t.Fatalf("reserved quote=%.8f, want 50.05", reservation.QuoteUSD)
	}

	trader.Case3AObligations["obligation-1"].ActiveOrderID = "exchange-order-1"
	if err := trader.rebuildDerivedResourceLedgerLocked(100); err != nil {
		t.Fatal(err)
	}
	reservation, ok = trader.resourceManager.Reservation("case3a:obligation-1")
	if !ok || !reservation.Informational || reservation.QuoteUSD != 0 {
		t.Fatalf("exchange-owned obligation must not double reserve: %+v", reservation)
	}
}

func TestWaitingForFundsObligationCanReenterAllocation(t *testing.T) {
	decisions := evaluateCase3AObligationResurrections(
		100,
		0.001,
		[]Case3AObligationSnapshot{{
			ObligationID: "obligation-1",
			Side:         SideBuy, TargetPrice: 101,
			RemainingBase: 0.5,
			Status:        Case3AObligationWaitingForFunds,
		}},
	)
	if len(decisions) != 1 || decisions[0].Case3AObligationID != "obligation-1" {
		t.Fatalf("waiting-for-funds obligation did not reenter allocation: %+v", decisions)
	}
}

package main

import (
	"math"
	"strings"
	"testing"
)

func TestVersion201RefundObligationLifecycle(t *testing.T) {
	tr := newOrderingTrader(t, &orderingBroker{pollObserved: make(chan bool, 1)})
	tr.cfg.PersistState = false
	tr.stateFile = ""

	first := tr.upsertRefundObligationLocked(RefundObligation{
		ID: "refund-small", ShortageSide: SideBuy,
		OriginalUSD: 40, RemainingUSD: 40,
	})
	second := tr.upsertRefundObligationLocked(RefundObligation{
		ID: "refund-large", ShortageSide: SideBuy,
		OriginalUSD: 100, RemainingUSD: 100,
	})
	if first == nil || second == nil ||
		first.ServiceSide != SideSell || second.ServiceSide != SideSell {
		t.Fatalf("refund service-side derivation changed: first=%+v second=%+v", first, second)
	}
	if tr.refundBuyUSD != 100 {
		t.Fatalf("compatibility summary=%v want maximum outstanding 100", tr.refundBuyUSD)
	}

	id, reserved := tr.reserveRefundObligationLocked(SideSell, 60, "decision")
	if id != "refund-large" || reserved != 60 {
		t.Fatalf("refund selection changed: id=%q reserved=%v", id, reserved)
	}
	intent := &PendingIntent{
		RefundObligationID: id,
		RefundPortionUSD:   reserved,
	}
	tr.settleRefundFillLocked(intent, 25)
	remaining := tr.RefundObligations[id]
	if remaining == nil || math.Abs(remaining.RemainingUSD-75) > 1e-12 ||
		remaining.ReservedUSD != 0 || remaining.Status != RefundObligationWaiting {
		t.Fatalf("partial refund settlement changed: %+v", remaining)
	}

	_, reserved = tr.reserveRefundObligationLocked(SideSell, 75, "decision-2")
	intent.RefundPortionUSD = reserved
	tr.settleRefundFillLocked(intent, 75)
	if _, exists := tr.RefundObligations[id]; exists {
		t.Fatal("fully serviced refund obligation was not deleted")
	}
}

func TestVersion201RefundReservationReleasePreservesDebt(t *testing.T) {
	tr := newOrderingTrader(t, &orderingBroker{pollObserved: make(chan bool, 1)})
	obligation := tr.upsertRefundObligationLocked(RefundObligation{
		ID: "refund-release", ShortageSide: SideSell,
		OriginalUSD: 50, RemainingUSD: 50,
	})
	id, reserved := tr.reserveRefundObligationLocked(SideBuy, 30, "decision")
	intent := &PendingIntent{RefundObligationID: id, RefundPortionUSD: reserved}
	tr.releaseRefundReservationLocked(intent)
	if obligation.RemainingUSD != 50 || obligation.ReservedUSD != 0 ||
		obligation.Status != RefundObligationWaiting || obligation.ActiveDecisionID != "" {
		t.Fatalf("reservation release changed durable debt: %+v", obligation)
	}
}

func TestVersion201RefundRetryThrottleIsExitSpecificAndDurable(t *testing.T) {
	section := sourceSection(
		t, "trader.go",
		"refundExitObligationID := fmt.Sprintf(",
		"t.mu.Unlock()",
	)
	if !strings.Contains(section, "time.Now().UTC().Before(obligation.NextRetryAt)") ||
		!strings.Contains(section, "EXIT-DEFER-REFUND") {
		t.Fatal("exit Refund retry throttle no longer checks durable NextRetryAt")
	}

	creation := sourceSection(
		t, "trader.go",
		"if marketEntryErrorCode(err) == EntryProduceErrInsufficientBalance",
		"return \"\", false, fmt.Errorf(",
	)
	if !strings.Contains(creation, "NextRetryAt:    time.Now().UTC().Add(30 * time.Second)") {
		t.Fatal("exit funding shortfall no longer creates the 30-second Refund retry delay")
	}
}

func TestVersion201RefundAttachmentRemainsAfterCoreAllocation(t *testing.T) {
	section := sourceSection(
		t, "producer_resource_coordinator.go",
		"func (c ProducerResourceCoordinator) Allocate(",
		"func (t *Trader) buildProducerResourceRequestLocked(",
	)
	requireSourceOrder(t, section,
		"core sizing/funding is resolved",
		"refundAvailable := math.Max(0, groupAvailable-usedCore)",
		"refundPart := snapDownResource",
	)
}

func TestVersion201RepriceContract(t *testing.T) {
	section := sourceSection(
		t, "trader.go",
		"func (t *Trader) maybeRepriceOnce(",
		"type RepriceObservation struct",
	)
	requireSourceOrder(t, section,
		"if !t.cfg.RepriceEnable",
		"if t.cfg.RepriceMaxCount > 0",
		"t.cfg.RepriceMaxDriftBps",
		"t.broker.CancelOrder(",
		"t.broker.PlaceLimitPostOnly(",
		"observation.Accepted = true",
		"intent.LimitPx = newLimitPx",
	)

	poller := sourceSection(
		t, "trader.go",
		"addRepriceProducerEvent := func(",
		"// Entry Drain result",
	)
	for _, fragment := range []string{
		"ProducerStageRepriced", "reprice_sequence=", "old_order_id=", "new_order_id=",
	} {
		if !strings.Contains(poller, fragment) {
			t.Fatalf("repricing lifecycle fragment missing: %s", fragment)
		}
	}
}

func TestVersion201PartialFillAndUncertaintyContracts(t *testing.T) {
	entry := sourceSection(
		t, "trader.go",
		"generic poll/reprice lifecycle",
		"// Entry Drain result",
	)
	for _, fragment := range []string{
		"AccumBase", "AccumQuote", "AccumFeeUSD", "ProducerStageFilled",
		"ProducerStageCancelRequested", "ProducerStageCleanupCancelled",
	} {
		if !strings.Contains(entry, fragment) {
			t.Fatalf("entry lifecycle fragment missing: %s", fragment)
		}
	}

	quarantine := sourceSection(
		t, "trader.go",
		"func (t *Trader) reconcileResourceQuarantines(",
		"func (t *Trader) pendingProducerCountsNoLock(",
	)
	for _, fragment := range []string{
		"t.resourceManager.Quarantines()", "GetOrder", "Release", "saveState",
	} {
		if !strings.Contains(quarantine, fragment) {
			t.Fatalf("resource reconciliation fragment missing: %s", fragment)
		}
	}
}

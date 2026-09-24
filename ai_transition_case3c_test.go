package main

import (
	"math"
	"testing"
	"time"
)

func TestNetCreditedBaseChargesBuyCommissionOnce(t *testing.T) {
	if got := netCreditedBase(SideBuy, 0.001, 0.000001); math.Abs(got-0.000999) > 1e-12 {
		t.Fatalf("BUY net base=%0.12f, want 0.000999", got)
	}
	if got := netCreditedBase(SideSell, 0.001, 0.000001); got != 0.001 {
		t.Fatalf("SELL base changed: %0.12f", got)
	}
}

func TestAggregatedPartialFillsPreserveBaseCommission(t *testing.T) {
	placed := placedOrderFromAggregate(0.001, 85.0, 0.085, 0.000001)
	if placed.CommissionBase != 0.000001 {
		t.Fatalf("commission base=%0.12f", placed.CommissionBase)
	}
	if got := netCreditedBase(SideBuy, placed.BaseSize, placed.CommissionBase); math.Abs(got-0.000999) > 1e-12 {
		t.Fatalf("net aggregated base=%0.12f, want 0.000999", got)
	}
}

func TestCase3CRolloverDoesNotDoubleCountTriggerLoss(t *testing.T) {
	o := &Case3AObligation{
		RecoveryOriginalUSD:  2,
		RecoveryRemainingUSD: 2,
		UnrealizedTriggerUSD: 2,
	}
	applyCase3CRolloverPnL(o, -2.5)
	if math.Abs(o.RecoveryRemainingUSD-2.5) > 1e-9 {
		t.Fatalf("remaining=%f, want 2.5", o.RecoveryRemainingUSD)
	}
	if o.UnrealizedTriggerUSD != 0 {
		t.Fatalf("preview=%f, want zero", o.UnrealizedTriggerUSD)
	}
	applyCase3CRolloverPnL(o, 1.25)
	if math.Abs(o.RecoveryRemainingUSD-1.25) > 1e-9 {
		t.Fatalf("remaining after recovery=%f, want 1.25", o.RecoveryRemainingUSD)
	}
}

func TestAITransitionRolloverAttemptIsIdempotent(t *testing.T) {
	trader := &Trader{producerHistory: make(map[EntryProducer]*ProducerHistory)}
	at := time.Date(2026, 9, 23, 14, 10, 49, 0, time.UTC)
	if !trader.recordAITransitionRolloverEntryLocked(
		"66867924743", SideSell, "ai_transition_rollover_entry", at, 85035.59, 0.0006, 51.02,
	) {
		t.Fatal("first rollover attempt was not recorded")
	}
	if trader.recordAITransitionRolloverEntryLocked(
		"66867924743", SideSell, "duplicate", at, 85035.59, 0.0006, 51.02,
	) {
		t.Fatal("duplicate rollover attempt was recorded")
	}
	history := trader.producerHistory[EntryProducerAITransitionTrader]
	if history == nil || len(history.Attempts) != 1 {
		t.Fatalf("attempts=%v, want exactly one", history)
	}
	for _, attempt := range history.Attempts {
		for _, stage := range []ProducerStage{
			ProducerStageDecision, ProducerStageExchangeAccepted,
			ProducerStageFilled, ProducerStageCommitted,
		} {
			if event, ok := attempt.Events[stage]; !ok || event.OrderID != "66867924743" {
				t.Fatalf("stage %s missing canonical order correlation", stage)
			}
		}
	}
}

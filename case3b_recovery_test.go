package main

import "testing"

func TestCase3BResurrectionMirrorsCase3ADownward(t *testing.T) {
	snapshot := Case3AObligationSnapshot{
		ObligationID:         "case3b-test",
		OriginDecisionID:     "origin",
		SourceEntryOrderID:   "source-entry",
		SourceExitOrderID:    "source-exit",
		Side:                 SideBuy,
		RecoveryMethod:       RecoveryByProfitTarget,
		TargetPrice:          100,
		RemainingBase:        2,
		RecoveryRemainingUSD: 5,
		ProfitGateUSD:        1,
		Status:               Case3AObligationWaitingForTarget,
	}

	if got := evaluateCase3BObligationResurrections(101, 0, []Case3AObligationSnapshot{snapshot}); len(got) != 0 {
		t.Fatalf("Case3B resurrected above its BUY target: %#v", got)
	}

	got := evaluateCase3BObligationResurrections(99, 0, []Case3AObligationSnapshot{snapshot})
	if len(got) != 1 {
		t.Fatalf("Case3B decisions=%d, want 1", len(got))
	}
	if got[0].Producer != EntryProducerCase3BReplacement || got[0].Signal != Buy {
		t.Fatalf("producer=%s signal=%s, want Case3BReplacement BUY", got[0].Producer, got[0].Signal)
	}
	if got[0].Case3AObligationID != snapshot.ObligationID {
		t.Fatalf("obligation=%q, want %q", got[0].Case3AObligationID, snapshot.ObligationID)
	}
}

func TestRecoveryObligationMapsAreSideSpecific(t *testing.T) {
	trader := &Trader{
		Case3AObligations:         map[string]*Case3AObligation{},
		Case3BObligations:         map[string]*Case3AObligation{},
		PendingReplacementRetries: map[string]PendingReplacementRetry{},
		PendingCase3BRetries:      map[string]PendingReplacementRetry{},
	}

	case3A, _ := trader.recoveryObligationMapsLocked(EntryProducerCase3AReplacement)
	case3B, _ := trader.recoveryObligationMapsLocked(EntryProducerCase3BReplacement)
	case3A["same-id"] = &Case3AObligation{Side: SideSell}
	case3B["same-id"] = &Case3AObligation{Side: SideBuy}

	if case3A["same-id"].Side != SideSell || case3B["same-id"].Side != SideBuy {
		t.Fatal("Case3A and Case3B obligation ownership was not isolated")
	}
}

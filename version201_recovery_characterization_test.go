package main

import (
	"errors"
	"math"
	"os"
	"strings"
	"testing"
)

func recoveryIntent(id string) *PendingIntent {
	return &PendingIntent{
		Enabled:            true,
		DecisionID:         id,
		ObligationID:       id,
		Producer:           EntryProducerCase3AReplacement,
		Side:               SideSell,
		SourceEntryOrderID: "source-entry",
		SourceExitOrderID:  "source-exit",
		LimitPx:            100,
		BaseAtLimit:        10,
		RecoveryNetUSD:     4,
		ProfitGateUSD:      1,
	}
}

func recoveryTrader(t *testing.T, status Case3AObligationStatus) (*Trader, *PendingIntent) {
	t.Helper()
	broker := &orderingBroker{pollObserved: make(chan bool, 1)}
	tr := newOrderingTrader(t, broker)
	tr.cfg.PersistState = false
	tr.stateFile = ""
	intent := recoveryIntent("recovery-characterization")
	tr.Case3AObligations[intent.ObligationID] = &Case3AObligation{
		ObligationID:         intent.ObligationID,
		OriginDecisionID:     intent.DecisionID,
		SourceEntryOrderID:   intent.SourceEntryOrderID,
		SourceExitOrderID:    intent.SourceExitOrderID,
		Side:                 intent.Side,
		RecoveryMethod:       RecoveryByProfitTarget,
		TargetPrice:          intent.LimitPx,
		TargetBase:           10,
		RemainingBase:        10,
		RecoveryOriginalUSD:  4,
		RecoveryRemainingUSD: 4,
		ProfitGateUSD:        1,
		Status:               status,
		ActiveOrderID:        "active-order",
		ActiveDecisionID:     intent.DecisionID,
	}
	return tr, intent
}

func sourceSection(t *testing.T, path, start, end string) string {
	t.Helper()

	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	source := string(data)
	startAt := strings.Index(source, start)
	if startAt < 0 {
		t.Fatalf("start marker %q not found in %s", start, path)
	}
	endAt := strings.Index(source[startAt+len(start):], end)
	if endAt < 0 {
		t.Fatalf("end marker %q not found after %q in %s", end, start, path)
	}

	return source[startAt : startAt+len(start)+endAt]
}

func requireSourceOrder(t *testing.T, source string, fragments ...string) {
	t.Helper()

	last := -1
	for _, fragment := range fragments {
		at := strings.Index(source, fragment)
		if at < 0 {
			t.Fatalf("required source fragment not found: %q", fragment)
		}
		if at <= last {
			t.Fatalf("source fragment %q appears out of order", fragment)
		}
		last = at
	}
}

func TestVersion201DeferredModeBRetryIsConsumedBeforeSubmission(t *testing.T) {
	section := sourceSection(
		t,
		"step.go",
		"// PendingReplacementRetries owns exactly one deferred Mode B attempt",
		"// Once the one-time deferred Mode B retry is exhausted",
	)

	requireSourceOrder(t, section,
		"delete(t.PendingReplacementRetries, obligationID)",
		"obligation.Status = Case3AObligationReconcile",
		"obligation.ActiveOrderID = \"deferred_mode_b_submission_in_progress\"",
		"if err := t.saveStateNoLock(); err != nil",
		"t.mu.Unlock()",
		"orderID, retryErr := t.startCase3AReplacement(ctx, &repl, attempt)",
		"t.mu.Lock()",
		"t.recordProducerAttemptLocked(attempt)",
	)
}

func TestVersion201DeferredModeBSuccessIsOwnedByPendingRegistration(t *testing.T) {
	retrySection := sourceSection(
		t,
		"step.go",
		"// PendingReplacementRetries owns exactly one deferred Mode B attempt",
		"// Once the one-time deferred Mode B retry is exhausted",
	)
	if !strings.Contains(retrySection, "if retryErr == nil") ||
		!strings.Contains(retrySection, "registerPendingEntry() has already moved the obligation to ready") {
		t.Fatal("successful deferred Mode B handoff no longer delegates to pending registration")
	}

	registration := sourceSection(
		t,
		"trader.go",
		"if entry.Producer == EntryProducerCase3AReplacement",
		"// log.Printf(\n\t// \"[TRACE] pending.register",
	)
	requireSourceOrder(t, registration,
		"obligation.Status = Case3AObligationReady",
		"obligation.ActiveOrderID = orderID",
		"delete(t.PendingReplacementRetries, obligation.ObligationID)",
	)
}

func TestVersion201DeferredModeBFailureHandoff(t *testing.T) {
	section := sourceSection(
		t,
		"step.go",
		"if retryErr == nil",
		"// Once the one-time deferred Mode B retry is exhausted",
	)
	requireSourceOrder(t, section,
		"obligation.ActiveDecisionID = \"\"",
		"obligation.ActiveOrderID = \"\"",
		"obligation.AttemptCount++",
		"if case3ADeferredRetryNeedsReconcile(retryErr)",
		"obligation.Status = Case3AObligationReconcile",
		"obligation.Status = Case3AObligationActive",
	)
}

func TestVersion201DeferredModeBUncertaintyClassification(t *testing.T) {
	if case3ADeferredRetryNeedsReconcile(nil) {
		t.Fatal("nil error must not require reconciliation")
	}
	if !case3ADeferredRetryNeedsReconcile(errors.New("unclassified transport failure")) {
		t.Fatal("an unclassified failure must remain quarantined for reconciliation")
	}

	reconcileCodes := []EntryProduceErrorCode{
		EntryProduceErrSubmitTimeout,
		EntryProduceErrSubmitNetworkFailed,
		EntryProduceErrCleanupCancel,
	}
	for _, code := range reconcileCodes {
		err := &EntryProduceError{Code: code, Err: errors.New("characterization")}
		if !case3ADeferredRetryNeedsReconcile(err) {
			t.Fatalf("error code %s must require reconciliation", code)
		}
	}

	activeCodes := []EntryProduceErrorCode{
		EntryProduceErrInsufficientBalance,
		EntryProduceErrPostOnlyRejected,
		EntryProduceErrExchangeRejected,
	}
	for _, code := range activeCodes {
		err := &EntryProduceError{Code: code, Err: errors.New("characterization")}
		if case3ADeferredRetryNeedsReconcile(err) {
			t.Fatalf("error code %s must hand execution to the active obligation", code)
		}
	}
}

func TestVersion201PendingRetryPreventsEarlyResurrection(t *testing.T) {
	section := sourceSection(
		t,
		"step.go",
		"// Once the one-time deferred Mode B retry is exhausted",
		"case3AObligationSnapshots := t.case3AObligationSnapshotsLocked()",
	)
	retryGuard := "if _, retryPending := t.PendingReplacementRetries[obligation.ObligationID]; retryPending {\n\t\t\tcontinue\n\t\t}"
	if !strings.Contains(section, retryGuard) {
		t.Fatal("pending deferred Mode B retry no longer prevents early resurrection")
	}
	requireSourceOrder(t, section,
		retryGuard,
		"if obligation.Status == Case3AObligationWaiting",
		"obligation.Status = Case3AObligationActive",
	)
}

func TestVersion201RecoveryPartialFillApportionsRecovery(t *testing.T) {
	tr, intent := recoveryTrader(t, Case3AObligationReady)
	obligation, applied := tr.prepareCase3AObligationFillLocked(intent, 2)
	if obligation == nil {
		t.Fatal("partial fill did not resolve its obligation")
	}
	if math.Abs(applied-0.8) > 1e-12 || math.Abs(intent.RecoveryNetUSD-0.8) > 1e-12 {
		t.Fatalf("partial recovery apportionment changed: applied=%v intent=%v", applied, intent.RecoveryNetUSD)
	}

	tr.commitCase3AObligationFillLocked(intent, "partial-order", 2, applied)
	obligation = tr.Case3AObligations[intent.ObligationID]
	if obligation == nil {
		t.Fatal("partial fill incorrectly completed the obligation")
	}
	if math.Abs(obligation.RemainingBase-8) > 1e-12 ||
		math.Abs(obligation.RecoveryRemainingUSD-3.2) > 1e-12 {
		t.Fatalf("partial obligation progress changed: %+v", obligation)
	}
	if obligation.Status != Case3AObligationActive {
		t.Fatalf("partial initial attempt status=%q want active", obligation.Status)
	}
	if obligation.ActiveOrderID != "" || obligation.ActiveDecisionID != "" {
		t.Fatalf("partial fill retained active attempt identity: %+v", obligation)
	}
}

func TestVersion201ResurrectionPartialFillReturnsToTargetWait(t *testing.T) {
	tr, intent := recoveryTrader(t, Case3AObligationActive)
	_, applied := tr.prepareCase3AObligationFillLocked(intent, 2)
	tr.commitCase3AObligationFillLocked(intent, "resurrection-partial", 2, applied)

	obligation := tr.Case3AObligations[intent.ObligationID]
	if obligation == nil || obligation.Status != Case3AObligationWaitingForTarget {
		t.Fatalf("partial resurrection did not return to target wait: %+v", obligation)
	}
}

func TestVersion201RecoveryFullCommitDeletesObligationAndRetry(t *testing.T) {
	tr, intent := recoveryTrader(t, Case3AObligationReady)
	tr.PendingReplacementRetries[intent.ObligationID] = PendingReplacementRetry{
		ObligationID: intent.ObligationID,
		Replacement:  *intent,
	}
	_, applied := tr.prepareCase3AObligationFillLocked(intent, 10)
	tr.commitCase3AObligationFillLocked(intent, "full-order", 10, applied)

	if _, exists := tr.Case3AObligations[intent.ObligationID]; exists {
		t.Fatal("fully committed recovery obligation was not deleted")
	}
	if _, exists := tr.PendingReplacementRetries[intent.ObligationID]; exists {
		t.Fatal("fully committed recovery left a deferred retry")
	}
}

func TestVersion201RecoveryReconciliationQuarantinesObligation(t *testing.T) {
	tr, intent := recoveryTrader(t, Case3AObligationActive)
	tr.PendingReplacementRetries[intent.ObligationID] = PendingReplacementRetry{
		ObligationID: intent.ObligationID,
		Replacement:  *intent,
	}

	tr.reconcileCase3AObligationLocked(intent)
	obligation := tr.Case3AObligations[intent.ObligationID]
	if obligation == nil || obligation.Status != Case3AObligationReconcile {
		t.Fatalf("reconciliation did not quarantine obligation: %+v", obligation)
	}
	if _, exists := tr.PendingReplacementRetries[intent.ObligationID]; exists {
		t.Fatal("reconciliation retained an executable deferred retry")
	}
	if got := evaluateCase3AObligationResurrections(200, 0.001, tr.case3AObligationSnapshotsLocked()); len(got) != 0 {
		t.Fatalf("reconciliation obligation became executable: %+v", got)
	}
}

func TestVersion201RecoveryResurrectionUsesDedicatedMarketRoute(t *testing.T) {
	section := sourceSection(
		t,
		"producer_parallel_entry.go",
		"case3AResurrected := strings.TrimSpace(req.Decision.Case3AObligationID) != \"\"",
		"// Preserve the one-shot recheck lifecycle",
	)
	requireSourceOrder(t, section,
		"if case3AResurrected",
		"bid, ask, bboErr := t.broker.GetBBO(ctx, t.cfg.ProductID)",
		"totalBuffer := takerFeeRate + case3AResurrectionSlippageBps/10000.0",
		"execution=market_resurrection",
		"wantLimit = false",
		"placed, err = t.broker.PlaceMarketQuote",
		"t.commitCase3AObligationFillLocked",
	)
}

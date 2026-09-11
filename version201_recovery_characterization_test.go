package main

import (
	"errors"
	"os"
	"strings"
	"testing"
)

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

package main

import (
	"os"
	"strings"
	"testing"
)

func TestVersion201ContinuationReferencesAreProducerAndSideScoped(t *testing.T) {
	tr := newOrderingTrader(t, &orderingBroker{pollObserved: make(chan bool, 1)})
	tr.setProducerContinuationReference(EntryProducerCase11APeakReversal, SideSell, 101)
	tr.setProducerContinuationReference(EntryProducerCase11BBottomReversal, SideBuy, 99)
	tr.setProducerContinuationReference(EntryProducerCase11BBottomReversal, SideSell, 102)

	snapshot := tr.producerContinuationReferencesSnapshot()
	tr.clearProducerContinuationReference(EntryProducerCase11BBottomReversal, SideBuy)
	if snapshot[EntryProducerCase11BBottomReversal][SideBuy] != 99 {
		t.Fatal("immutable continuation snapshot changed with live state")
	}
	if _, exists := tr.producerContinuationReferences[EntryProducerCase11BBottomReversal][SideBuy]; exists {
		t.Fatal("single producer-side continuation reference was not cleared")
	}
	if tr.producerContinuationReferences[EntryProducerCase11BBottomReversal][SideSell] != 102 {
		t.Fatal("opposite-side continuation reference was cleared")
	}

	tr.clearProducerContinuationSide(SideSell)
	if len(tr.producerContinuationReferences) != 0 {
		t.Fatalf("side-wide continuation reset left references: %+v", tr.producerContinuationReferences)
	}
}

func TestVersion201RecoveryCannotOwnContinuationReference(t *testing.T) {
	tr := newOrderingTrader(t, &orderingBroker{pollObserved: make(chan bool, 1)})
	tr.setProducerContinuationReference(EntryProducerCase3AReplacement, SideSell, 100)
	if len(tr.producerContinuationReferences) != 0 {
		t.Fatal("Case3A recovery incorrectly acquired an ordinary producer continuation reference")
	}
}

func TestVersion201RunnerHelpersPreserveMultipleRunnerIdentity(t *testing.T) {
	book := &SideBook{Lots: []*Position{{}, {}, {}}}
	addRunner(book, 0)
	addRunner(book, 2)
	addRunner(book, 2)
	if runnerCount(book) != 2 || !isRunner(book, 0) || !isRunner(book, 2) || isRunner(book, 1) {
		t.Fatalf("runner identity changed: %+v", book.RunnerIDs)
	}
}

func TestVersion201PyramidTransitionAndRebaseOrdering(t *testing.T) {
	section := sourceSection(
		t, "step.go",
		"// Only timer-extension maintenance from raw evaluation.",
		"// The parallel-entry processor owns admission",
	)
	requireSourceOrder(t, section,
		"t.applyPyramidRawTransitions(",
		"legacyDirection := evaluateLegacyDirection(",
		"t.applyPyramidDecisionTransitions(",
		"if hasBuyDecision",
		"t.applyPyramidRebaseTransactions(pyramidResult, Buy)",
		"if hasSellDecision",
		"t.applyPyramidRebaseTransactions(pyramidResult, Sell)",
	)
}

func TestVersion201ProducerSingleFlightIsEvaluatedInsideProducerCards(t *testing.T) {
	source := sourceSection(
		t, "entry_producers.go",
		"func applyCase15BDowntrendRecoveryBuyProducer(",
		"func applyCase15AUptrendRecoverySellProducer(",
	)
	if !strings.Contains(source, "downtrendRecoveryBuy := pending == 0") {
		t.Fatal("Case15B no longer owns pending single-flight inside its producer function")
	}

	all, err := sourceFile("entry_producers.go")
	if err != nil {
		t.Fatal(err)
	}
	if strings.Count(all, "pending == 0") < 8 {
		t.Fatal("ordinary producer-card single-flight coverage unexpectedly decreased")
	}
}

func sourceFile(path string) (string, error) {
	data, err := os.ReadFile(path)
	return string(data), err
}

func TestVersion201StartupRehydratesWithoutResubmission(t *testing.T) {
	section := sourceSection(
		t, "trader.go",
		"func (t *Trader) RehydratePending(",
		"// ---- Fail-fast helpers",
	)
	for _, fragment := range []string{
		"entries := t.pendingEntriesSnapshot()",
		"ord, err := t.broker.GetOrder(",
		"t.startEntryPoller(",
		"exits := t.pendingExitsSnapshot()",
		"lot.FixedTPOrderID = orderID",
		"go t.watchPendingExit(",
	} {
		if !strings.Contains(section, fragment) {
			t.Fatalf("startup reconstruction fragment missing: %s", fragment)
		}
	}
	if strings.Contains(section, "PlaceLimitPostOnly") || strings.Contains(section, "PlaceMarketQuote") {
		t.Fatal("startup reconstruction may submit a new exchange order")
	}
}

func TestVersion201ProducerHistoryPruningProtectsExposure(t *testing.T) {
	section := sourceSection(
		t, "observability.go",
		"func (t *Trader) pruneProducerHistoryLocked(",
		"func (t *Trader) saveProducerHistoryNoLock(",
	)
	for _, fragment := range []string{
		"ProducerHistoryRetention", "filled &&", "!refundConsumed",
		"t.producerEntryOrderLiveLocked(", "if !exited", "exitedEvent.Time",
		"t.foldProducerAttemptEconomicsLocked(", "t.pruneProducerHistoryCountCapLocked(",
	} {
		if !strings.Contains(section, fragment) {
			t.Fatalf("producer-history retention fragment missing: %s", fragment)
		}
	}
}

func TestVersion201TickOrchestrationConcurrencyContract(t *testing.T) {
	stepSection := sourceSection(
		t, "step.go",
		"// ResourceSnapshot remains the authoritative funding view.",
		"return t.processParallelProducerEntriesLocked(",
	)
	requireSourceOrder(t, stepSection,
		"resourceSnapshot, resourceSnapshotOK :=",
		"go func() {\n\t\taiCh <- t.evaluateAI(signalHistory)",
		"go func() {\n\t\tmacdSnapCh <- t.evaluateMACDSnapshot(execHistory)",
		"go func() {\n\t\temaCh <- t.evaluateEMAPatternSnapshot(execHistory)",
		"pyramidRaw :=",
		"aiResult := <-aiCh",
		"macdSnapshot := <-macdSnapCh",
		"emaResult := <-emaCh",
		"independentExitResults = resultsCh",
		"t.fanOutExits(ctx, livePrice, candidates, hotStart)",
	)

	entrySection := sourceSection(
		t, "producer_parallel_entry.go",
		"// The full plan and all transient reservations have been established",
		"func mergeIndependentExitResults(",
	)
	requireSourceOrder(t, entrySection,
		"t.mu.Unlock()",
		"startIndependentExits(true)",
		"go func() {",
		"t.executeProducerAllocation(",
		"ordered[result.index] = result",
		"t.saveProducerHistoryNoLock()",
		"return mergeIndependentExitResults",
	)
}

func TestVersion201StateSnapshotRetainsAllOperationalOwners(t *testing.T) {
	section := sourceSection(
		t, "trader.go",
		"func (t *Trader) snapshotStateLocked() BotState",
		"// saveStateFrom writes",
	)
	for _, fragment := range []string{
		"PendingEntries:", "PendingExits:", "RefundObligations:",
		"Case3AObligations:", "PendingReplacementRetries:",
		"ResourceLedger:", "ProducerContinuationReferences:",
	} {
		if !strings.Contains(section, fragment) {
			t.Fatalf("state snapshot owner missing: %s", fragment)
		}
	}
}

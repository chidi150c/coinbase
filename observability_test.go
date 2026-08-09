package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// TestProducerHistoryBuySimulation verifies the Producer State Manager
// independently of the exchange/broker path.
//
// It simulates one BUY producer attempt with:
//
//	produced -> pending
//
// then records it through the same higher-level history helper,
// persists producerHistory to the independent producer-history state file,
// reloads the JSON from disk, and verifies that the producer, DecisionID,
// stages, side, and order ID survived persistence.
//
// This test intentionally does NOT call startProducerBuyEntry() and does
// NOT place any broker/exchange order. Its purpose is to answer the current
// question: "Does producerHistory get recorded and does the independent
// producer-history file actually get created?"
func TestProducerHistoryBuySimulation(t *testing.T) {
	stateFile := filepath.Join(
		t.TempDir(),
		"producer_history.json",
	)

	trader := &Trader{
		producerHistoryFile: stateFile,
		producerHistory: make(
			map[EntryProducer]*ProducerHistory,
		),
	}

	// Use a producer value directly so this test does not depend on any
	// particular strategy producer constant being present in this build.
	producer := EntryProducer("ProducerHistoryBuySimulation")

	createdAt := time.Now().UTC()

	decisionID := FormatDecisionID(
		producer,
		createdAt,
	)

	const orderID = "SIM-BUY-ORDER-001"

	attempt := &ProducerAttempt{
		DecisionID: decisionID,
		CreatedAt:  createdAt,
		Producer:   producer,
		Side:       "BUY",
		Events:     make(map[ProducerStage]ProducerEvent),
	}

	// Source-wrapper-owned lifecycle event.
	attempt.Events[ProducerStageProduced] = ProducerEvent{
		Time:       createdAt,
		CreatedAt:  createdAt,
		Producer:   producer,
		Side:       "BUY",
		Stage:      ProducerStageProduced,
		DecisionID: decisionID,
		Reason:     "producer_history_buy_simulation",
	}

	// produceEntry()-owned successful pending lifecycle event.
	pendingAt := createdAt.Add(time.Millisecond)

	attempt.Events[ProducerStagePending] = ProducerEvent{
		Time:       pendingAt,
		CreatedAt:  createdAt,
		Producer:   producer,
		Side:       "BUY",
		Stage:      ProducerStagePending,
		DecisionID: decisionID,
		OrderID:    orderID,
		Reason:     "producer_history_buy_simulation",
	}

	/*
		Simulate the higher-level caller boundary.

		recordProducerAttemptLocked() requires t.mu to already be held.
		saveProducerHistoryNoLock() is called while the same state is stable.
	*/
	trader.mu.Lock()

	trader.recordProducerAttemptLocked(
		attempt,
	)

	if err := trader.saveProducerHistoryNoLock(); err != nil {
		trader.mu.Unlock()

		t.Fatalf(
			"saveProducerHistoryNoLock() failed: %v",
			err,
		)
	}

	trader.mu.Unlock()

	// The main purpose of this test: prove that persistence created the
	// independent producer-history file.
	info, err := os.Stat(stateFile)
	if err != nil {
		t.Fatalf(
			"producer history file was not created: path=%s err=%v",
			stateFile,
			err,
		)
	}

	if info.Size() == 0 {
		t.Fatalf(
			"producer history file is empty: path=%s",
			stateFile,
		)
	}

	raw, err := os.ReadFile(stateFile)
	if err != nil {
		t.Fatalf(
			"read producer history file: %v",
			err,
		)
	}

	var restored map[EntryProducer]*ProducerHistory

	if err := json.Unmarshal(raw, &restored); err != nil {
		t.Fatalf(
			"unmarshal producer history file: %v\nfile=%s",
			err,
			string(raw),
		)
	}

	history := restored[producer]
	if history == nil {
		t.Fatalf(
			"producer missing after persistence: producer=%s",
			producer,
		)
	}

	if history.Attempts == nil {
		t.Fatalf(
			"producer attempts map missing after persistence: producer=%s",
			producer,
		)
	}

	restoredAttempt := history.Attempts[decisionID]
	if restoredAttempt == nil {
		t.Fatalf(
			"attempt missing after persistence: producer=%s decision_id=%s",
			producer,
			decisionID,
		)
	}

	if restoredAttempt.DecisionID != decisionID {
		t.Fatalf(
			"DecisionID mismatch: got=%s want=%s",
			restoredAttempt.DecisionID,
			decisionID,
		)
	}

	if restoredAttempt.Producer != producer {
		t.Fatalf(
			"Producer mismatch: got=%s want=%s",
			restoredAttempt.Producer,
			producer,
		)
	}

	if restoredAttempt.Side != "BUY" {
		t.Fatalf(
			"Side mismatch: got=%s want=BUY",
			restoredAttempt.Side,
		)
	}

	producedEvent, ok :=
		restoredAttempt.Events[ProducerStageProduced]

	if !ok {
		t.Fatalf(
			"produced event missing: decision_id=%s",
			decisionID,
		)
	}

	if producedEvent.Stage != ProducerStageProduced {
		t.Fatalf(
			"produced stage mismatch: got=%s want=%s",
			producedEvent.Stage,
			ProducerStageProduced,
		)
	}

	pendingEvent, ok :=
		restoredAttempt.Events[ProducerStagePending]

	if !ok {
		t.Fatalf(
			"pending event missing: decision_id=%s",
			decisionID,
		)
	}

	if pendingEvent.Stage != ProducerStagePending {
		t.Fatalf(
			"pending stage mismatch: got=%s want=%s",
			pendingEvent.Stage,
			ProducerStagePending,
		)
	}

	if pendingEvent.OrderID != orderID {
		t.Fatalf(
			"pending OrderID mismatch: got=%s want=%s",
			pendingEvent.OrderID,
			orderID,
		)
	}

	t.Logf(
		"PASS producer=%s side=BUY decision_id=%s order_id=%s events=%d file=%s bytes=%d",
		producer,
		decisionID,
		orderID,
		len(restoredAttempt.Events),
		stateFile,
		info.Size(),
	)
}

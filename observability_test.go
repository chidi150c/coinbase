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

func TestCombineEntryRawMaterialsProducesBuy(t *testing.T) {
	trader := &Trader{
		MarketRegime:     RegimeNormal,
		RegimeMultiplier: 1.0,
	}

	/*
		Force Case11B — Bottom-Reversal BUY.

		Case11B requires:
		  macd.LinePrev6 <= -macd.EPS + 15
		  ema.LowBottom == true
		  pyramid.Buy.GatePassed == true

		With EPS=50:
		  -EPS + 15 = -35

		LinePrev6=-60 therefore satisfies the MACD condition.
	*/
	ai := AIResult{
		Raw:           Flat,
		PUp:           0.50,
		BuyThreshold:  0.40,
		SellThreshold: 0.56,
		Confidence:    0.50,
	}

	macd := MACDResult{
		EPS:       50.0,
		LinePrev6: -60.0,
		Line:      -20.0,
		Hist:      -5.0,
	}

	ema := EMAPatternResult{
		LowBottom: true,
	}

	pyramid := PyramidResult{
		Buy: PyramidSideResult{
			SpacingPass: true,
			AdversePass: true,
			GatePassed:  true,
			Reason:      "simulated_case11B_buy_gate",
		},
	}

	equity := EquityResult{
		BuyTrigger:  false,
		SellTrigger: false,
	}

	price := 65000.00

	pendingCounts := PendingProducerCounts{
		ByProducer: make(
			map[EntryProducer]map[OrderSide]int,
		),
	}

	entryDecision := trader.combineEntryRawMaterials(
		ai,
		macd,
		ema,
		pyramid,
		equity,
		price,
		pendingCounts,
	)

	if entryDecision.Signal != Buy {
		t.Fatalf(
			"expected BUY, got=%s producer=%s reason=%q",
			entryDecision.Signal,
			entryDecision.Producer,
			entryDecision.ProducerReason,
		)
	}

	if entryDecision.Producer !=
		EntryProducerCase11BBottomReversal {

		t.Fatalf(
			"expected producer=%s got=%s reason=%q",
			EntryProducerCase11BBottomReversal,
			entryDecision.Producer,
			entryDecision.ProducerReason,
		)
	}

	if !entryDecision.BottomReversalBuy {
		t.Fatal(
			"expected BottomReversalBuy=true",
		)
	}

	if !entryDecision.MACDPreBottomZone {
		t.Fatal(
			"expected MACDPreBottomZone=true",
		)
	}

	if !entryDecision.PyramidPass {
		t.Fatal(
			"expected PyramidPass=true",
		)
	}

	if entryDecision.PendingCancelPolicy !=
		PendingSignalCancelDisabled {

		t.Fatalf(
			"expected cancel policy=%s got=%s",
			PendingSignalCancelDisabled,
			entryDecision.PendingCancelPolicy,
		)
	}

	side, ok := entryDecision.SignalToSide()
	if !ok {
		t.Fatal(
			"BUY decision did not resolve to order side",
		)
	}

	if side != SideBuy {
		t.Fatalf(
			"expected SideBuy got=%s",
			side,
		)
	}

	t.Logf(
		"PASS combineEntryRawMaterials produced BUY "+
			"producer=%s side=%s final=%s "+
			"macd_pre_bottom=%t bottom_reversal_buy=%t "+
			"pyramid_pass=%t cancel_policy=%s reason=%q",
		entryDecision.Producer,
		side,
		entryDecision.Signal,
		entryDecision.MACDPreBottomZone,
		entryDecision.BottomReversalBuy,
		entryDecision.PyramidPass,
		entryDecision.PendingCancelPolicy,
		entryDecision.ProducerReason,
	)
}

func TestProducerFullLifecycleBuy(t *testing.T) {
	const (
		stateFile = "/tmp/producer_history_full_lifecycle.json"
		orderID   = "SIM-CASE11B-BUY-001"
	)

	// Start clean so this run proves that this test creates the file.
	if err := os.Remove(stateFile); err != nil &&
		!os.IsNotExist(err) {

		t.Fatalf(
			"remove previous producer history file: %v",
			err,
		)
	}

	trader := &Trader{
		MarketRegime:     RegimeNormal,
		RegimeMultiplier: 1.0,

		producerHistoryFile: stateFile,
		producerHistory: make(
			map[EntryProducer]*ProducerHistory,
		),
	}

	// ---------------------------------------------------------
	// 1. SIMULATE RAW MATERIALS
	//
	// Force Case11B — Bottom-Reversal BUY.
	//
	// Case11B requires:
	//
	//   LinePrev6 <= -EPS + 15
	//   LowBottom == true
	//   BUY Pyramid gate == true
	//
	// EPS=50:
	//
	//   -50 + 15 = -35
	//
	// LinePrev6=-60 therefore qualifies.
	// ---------------------------------------------------------

	ai := AIResult{
		Raw:           Flat,
		PUp:           0.50,
		BuyThreshold:  0.40,
		SellThreshold: 0.56,
		Confidence:    0.50,
	}

	macd := MACDResult{
		EPS:       50.0,
		LinePrev6: -60.0,
		Line:      -20.0,
		Hist:      -5.0,
	}

	ema := EMAPatternResult{
		LowBottom: true,
	}

	pyramid := PyramidResult{
		Buy: PyramidSideResult{
			SpacingPass: true,
			AdversePass: true,
			GatePassed:  true,
			Reason:      "simulated_case11B_buy_gate",
		},
	}

	equity := EquityResult{
		BuyTrigger:  false,
		SellTrigger: false,
	}

	price := 65000.00

	pendingCounts := PendingProducerCounts{
		ByProducer: make(
			map[EntryProducer]map[OrderSide]int,
		),
	}

	// ---------------------------------------------------------
	// 2. PRODUCER SELECTION
	// ---------------------------------------------------------

	entryDecision := trader.combineEntryRawMaterials(
		ai,
		macd,
		ema,
		pyramid,
		equity,
		price,
		pendingCounts,
	)

	if entryDecision.Signal != Buy {
		t.Fatalf(
			"producer selection did not produce BUY: "+
				"signal=%s producer=%s reason=%q",
			entryDecision.Signal,
			entryDecision.Producer,
			entryDecision.ProducerReason,
		)
	}

	if entryDecision.Producer !=
		EntryProducerCase11BBottomReversal {

		t.Fatalf(
			"unexpected producer: got=%s want=%s",
			entryDecision.Producer,
			EntryProducerCase11BBottomReversal,
		)
	}

	side, ok := entryDecision.SignalToSide()
	if !ok || side != SideBuy {
		t.Fatalf(
			"producer BUY did not resolve SideBuy: "+
				"ok=%t side=%s",
			ok,
			side,
		)
	}

	// ---------------------------------------------------------
	// 3. PRODUCER ATTEMPT CREATION
	//
	// This represents the source-wrapper boundary.
	//
	// CreatedAt and DecisionID are created once and are never
	// regenerated during the lifecycle.
	// ---------------------------------------------------------

	createdAt := time.Now().UTC()

	decisionID := FormatDecisionID(
		entryDecision.Producer,
		createdAt,
	)

	attempt := &ProducerAttempt{
		DecisionID: decisionID,
		CreatedAt:  createdAt,

		Producer: entryDecision.Producer,
		Side:     "BUY",

		Events: make(
			map[ProducerStage]ProducerEvent,
		),
	}

	// ---------------------------------------------------------
	// 4. STAGE = PRODUCED
	// ---------------------------------------------------------

	attempt.Events[ProducerStageProduced] =
		ProducerEvent{
			Time:      createdAt,
			CreatedAt: createdAt,

			Producer: entryDecision.Producer,
			Side:     "BUY",
			Stage:    ProducerStageProduced,

			DecisionID: decisionID,

			Reason: entryDecision.ProducerReason,
		}

	// Record + persist exactly as the higher-level owner does.
	trader.mu.Lock()

	trader.recordProducerAttemptLocked(
		attempt,
	)

	if err := trader.saveProducerHistoryNoLock(); err != nil {
		trader.mu.Unlock()

		t.Fatalf(
			"save after produced failed: %v",
			err,
		)
	}

	trader.mu.Unlock()

	// ---------------------------------------------------------
	// 5. STAGE = PENDING
	//
	// Simulate successful order submission + pending registration.
	// Same DecisionID; now there is an OrderID.
	// ---------------------------------------------------------

	pendingAt := time.Now().UTC()

	attempt.Events[ProducerStagePending] =
		ProducerEvent{
			Time:      pendingAt,
			CreatedAt: createdAt,

			Producer: entryDecision.Producer,
			Side:     "BUY",
			Stage:    ProducerStagePending,

			DecisionID: decisionID,
			OrderID:    orderID,

			Reason: entryDecision.ProducerReason,
		}

	trader.mu.Lock()

	trader.recordProducerAttemptLocked(
		attempt,
	)

	if err := trader.saveProducerHistoryNoLock(); err != nil {
		trader.mu.Unlock()

		t.Fatalf(
			"save after pending failed: %v",
			err,
		)
	}

	trader.mu.Unlock()

	// ---------------------------------------------------------
	// 6. STAGE = FILLED
	//
	// Simulate the poller reporting the maker BUY as filled.
	// ---------------------------------------------------------

	filledAt := time.Now().UTC()

	attempt.Events[ProducerStageFilled] =
		ProducerEvent{
			Time:      filledAt,
			CreatedAt: createdAt,

			Producer: entryDecision.Producer,
			Side:     "BUY",
			Stage:    ProducerStageFilled,

			DecisionID: decisionID,
			OrderID:    orderID,

			Reason: entryDecision.ProducerReason,
		}

	trader.mu.Lock()

	trader.recordProducerAttemptLocked(
		attempt,
	)

	if err := trader.saveProducerHistoryNoLock(); err != nil {
		trader.mu.Unlock()

		t.Fatalf(
			"save after filled failed: %v",
			err,
		)
	}

	trader.mu.Unlock()

	// ---------------------------------------------------------
	// 7. STAGE = COMMITTED
	//
	// Simulate the higher-level pending-entry drain committing the
	// filled BUY into trading position state.
	// ---------------------------------------------------------

	committedAt := time.Now().UTC()

	attempt.Events[ProducerStageCommitted] =
		ProducerEvent{
			Time:      committedAt,
			CreatedAt: createdAt,

			Producer: entryDecision.Producer,
			Side:     "BUY",
			Stage:    ProducerStageCommitted,

			DecisionID: decisionID,
			OrderID:    orderID,

			Reason: entryDecision.ProducerReason,
		}

	trader.mu.Lock()

	trader.recordProducerAttemptLocked(
		attempt,
	)

	if err := trader.saveProducerHistoryNoLock(); err != nil {
		trader.mu.Unlock()

		t.Fatalf(
			"save after committed failed: %v",
			err,
		)
	}

	trader.mu.Unlock()

	// ---------------------------------------------------------
	// 8. VERIFY THE PERSISTED PRODUCER STATE
	// ---------------------------------------------------------

	raw, err := os.ReadFile(stateFile)
	if err != nil {
		t.Fatalf(
			"producer history file not created: "+
				"file=%s err=%v",
			stateFile,
			err,
		)
	}

	var restored map[EntryProducer]*ProducerHistory

	if err := json.Unmarshal(
		raw,
		&restored,
	); err != nil {

		t.Fatalf(
			"unmarshal producer history: %v\n%s",
			err,
			string(raw),
		)
	}

	history :=
		restored[EntryProducerCase11BBottomReversal]

	if history == nil {
		t.Fatalf(
			"Case11B producer history missing",
		)
	}

	restoredAttempt :=
		history.Attempts[decisionID]

	if restoredAttempt == nil {
		t.Fatalf(
			"producer attempt missing: decision_id=%s",
			decisionID,
		)
	}

	// ---------------------------------------------------------
	// 9. VERIFY ALL FOUR LIFECYCLE STAGES
	// ---------------------------------------------------------

	requiredStages := []ProducerStage{
		ProducerStageProduced,
		ProducerStagePending,
		ProducerStageFilled,
		ProducerStageCommitted,
	}

	for _, stage := range requiredStages {
		event, exists :=
			restoredAttempt.Events[stage]

		if !exists {
			t.Fatalf(
				"lifecycle stage missing: "+
					"decision_id=%s stage=%s",
				decisionID,
				stage,
			)
		}

		if event.DecisionID != decisionID {
			t.Fatalf(
				"DecisionID changed during lifecycle: "+
					"stage=%s got=%s want=%s",
				stage,
				event.DecisionID,
				decisionID,
			)
		}

		if event.Producer !=
			EntryProducerCase11BBottomReversal {

			t.Fatalf(
				"producer changed during lifecycle: "+
					"stage=%s got=%s",
				stage,
				event.Producer,
			)
		}

		if stage != ProducerStageProduced &&
			event.OrderID != orderID {

			t.Fatalf(
				"OrderID mismatch: "+
					"stage=%s got=%s want=%s",
				stage,
				event.OrderID,
				orderID,
			)
		}
	}

	if len(restoredAttempt.Events) != 4 {
		t.Fatalf(
			"unexpected lifecycle event count: "+
				"got=%d want=4",
			len(restoredAttempt.Events),
		)
	}

	t.Logf(
		"PASS FULL PRODUCER LIFECYCLE "+
			"producer=%s "+
			"decision_id=%s "+
			"order_id=%s "+
			"stages=produced,pending,filled,committed "+
			"file=%s bytes=%d",
		restoredAttempt.Producer,
		restoredAttempt.DecisionID,
		orderID,
		stateFile,
		len(raw),
	)
}

package main

import (
	"encoding/json"
	"math"
	"os"
	"strings"
	"testing"
	"time"
)

type version201Golden struct {
	SourceCommit string `json:"source_commit"`
	BotVersion   int    `json:"bot_version"`
	Case16A      struct {
		Price                  float64
		RecentHigh             float64 `json:"recent_high"`
		Confidence             float64
		MACDPrev6              float64 `json:"macd_prev6"`
		MACDLine               float64 `json:"macd_line"`
		MACDHist               float64 `json:"macd_hist"`
		EMASpread              float64 `json:"ema_spread"`
		EMA2050                float64 `json:"ema2050"`
		Passes                 bool
		Producer, Signal, Tier string
		Priority               int
	} `json:"case16a_snapshot"`
	Allocation struct {
		Price           float64
		SpareQuote      float64 `json:"spare_quote"`
		HighRequested   float64 `json:"high_requested"`
		LowRequested    float64 `json:"low_requested"`
		HighAllocated   float64 `json:"high_allocated"`
		LowAllocated    float64 `json:"low_allocated"`
		EqualAAllocated float64 `json:"equal_a_allocated"`
		EqualBAllocated float64 `json:"equal_b_allocated"`
	} `json:"allocation"`
	Recovery struct {
		Target           float64
		TakerFeeRate     float64 `json:"taker_fee_rate"`
		SlippageBps      float64 `json:"slippage_bps"`
		SellBelowTrigger float64 `json:"sell_below_trigger"`
		SellAtTrigger    float64 `json:"sell_at_trigger"`
		BuyAboveTrigger  float64 `json:"buy_above_trigger"`
		BuyAtTrigger     float64 `json:"buy_at_trigger"`
	} `json:"recovery"`
}

func loadVersion201Golden(t *testing.T) version201Golden {
	t.Helper()
	b, err := os.ReadFile("testdata/version201_characterization.json")
	if err != nil {
		t.Fatal(err)
	}
	var g version201Golden
	if err := json.Unmarshal(b, &g); err != nil {
		t.Fatal(err)
	}
	if g.BotVersion != Version {
		t.Fatalf("fixture version=%d code version=%d", g.BotVersion, Version)
	}
	return g
}

func almostEqual(a, b float64) bool { return math.Abs(a-b) <= 1e-9 }

func TestVersion201Case16ASnapshotGolden(t *testing.T) {
	g := loadVersion201Golden(t)
	d := EntryDecision{}
	passed := applyCase16ANormalPeakRolloverSellProducer(
		&d,
		AIResult{Raw: Sell, Confidence: g.Case16A.Confidence},
		MACDResult{LinePrev6: g.Case16A.MACDPrev6, Line: g.Case16A.MACDLine, Hist: g.Case16A.MACDHist},
		EMAPatternResult{Spread: g.Case16A.EMASpread, EMA2050: g.Case16A.EMA2050},
		PyramidResult{Sell: PyramidSideResult{SpacingPass: true}},
		g.Case16A.Price, g.Case16A.RecentHigh, RegimeNormal,
		PendingProducerCounts{ByProducer: map[EntryProducer]map[OrderSide]int{}},
		ProducerContinuationReferences{},
	)
	if passed != g.Case16A.Passes || string(d.Producer) != g.Case16A.Producer || d.Signal.String() != g.Case16A.Signal || int(d.ProducerPriority) != g.Case16A.Priority || string(d.ProducerTier) != g.Case16A.Tier {
		t.Fatalf("Case16A mismatch: passed=%t decision=%+v", passed, d)
	}
	if !strings.Contains(d.ProducerReason, "reference_mode=first_recent_high_area") {
		t.Fatalf("missing first-entry evidence: %s", d.ProducerReason)
	}
}

func TestVersion201ProducerSingleFlightGolden(t *testing.T) {
	g := loadVersion201Golden(t)
	d := EntryDecision{}
	passed := applyCase16ANormalPeakRolloverSellProducer(&d, AIResult{Raw: Sell, Confidence: g.Case16A.Confidence}, MACDResult{LinePrev6: g.Case16A.MACDPrev6, Line: g.Case16A.MACDLine, Hist: g.Case16A.MACDHist}, EMAPatternResult{Spread: g.Case16A.EMASpread, EMA2050: g.Case16A.EMA2050}, PyramidResult{Sell: PyramidSideResult{SpacingPass: true}}, g.Case16A.Price, g.Case16A.RecentHigh, RegimeNormal, PendingProducerCounts{ByProducer: map[EntryProducer]map[OrderSide]int{EntryProducerCase16ANormalPeakRolloverSell: {SideSell: 1}}}, ProducerContinuationReferences{})
	if passed {
		t.Fatalf("pending single-flight admitted duplicate: %+v", d)
	}
}

func quoteRequest(producer EntryProducer, priority ProducerPriority, requested float64) ProducerResourceRequest {
	return ProducerResourceRequest{Producer: producer, Priority: priority, Side: SideBuy, ResourceKind: ResourceKindQuote, RequestedResource: requested, RequestedQuote: requested, CoreQuote: requested, MinimumResource: 1, ResourceStep: 1, ConsumesLotSlot: true, Intent: &PendingIntent{DecisionID: string(producer)}}
}

func TestVersion201AllocationPriorityGolden(t *testing.T) {
	g := loadVersion201Golden(t)
	s := ResourceSnapshot{Price: g.Allocation.Price, SpareQuote: g.Allocation.SpareQuote, MinNotional: 1, AvailableLotSlots: -1}
	plan := (ProducerResourceCoordinator{}).Allocate(s, []ProducerResourceRequest{quoteRequest(EntryProducerNormalLegacy, ProducerPriorityNormalLegacy, g.Allocation.LowRequested), quoteRequest(EntryProducerCase11APeakReversal, ProducerPriorityCase11A, g.Allocation.HighRequested)}, true)
	if len(plan.Allocations) != 2 {
		t.Fatalf("allocations=%d", len(plan.Allocations))
	}
	if plan.Allocations[0].Request.Producer != EntryProducerCase11APeakReversal || !almostEqual(plan.Allocations[0].AllocatedQuote, g.Allocation.HighAllocated) {
		t.Fatalf("high allocation=%+v", plan.Allocations[0])
	}
	if plan.Allocations[1].Request.Producer != EntryProducerNormalLegacy || !almostEqual(plan.Allocations[1].AllocatedQuote, g.Allocation.LowAllocated) || plan.Allocations[1].Status != AllocationPartial {
		t.Fatalf("low allocation=%+v", plan.Allocations[1])
	}
}

func TestVersion201EqualPriorityProportionalGolden(t *testing.T) {
	g := loadVersion201Golden(t)
	s := ResourceSnapshot{Price: g.Allocation.Price, SpareQuote: g.Allocation.SpareQuote, MinNotional: 1, AvailableLotSlots: -1}
	plan := (ProducerResourceCoordinator{}).Allocate(s, []ProducerResourceRequest{quoteRequest("B", 500, 80), quoteRequest("A", 500, 80)}, true)
	if len(plan.Allocations) != 2 || plan.Allocations[0].Request.Producer != "A" || !almostEqual(plan.Allocations[0].AllocatedQuote, g.Allocation.EqualAAllocated) || !almostEqual(plan.Allocations[1].AllocatedQuote, g.Allocation.EqualBAllocated) {
		t.Fatalf("proportional plan=%+v", plan.Allocations)
	}
}

func TestVersion201RefundAllocatesAfterCoreGolden(t *testing.T) {
	s := ResourceSnapshot{Price: 10, SpareQuote: 100, MinNotional: 1, AvailableLotSlots: -1}
	withRefund := quoteRequest("A", 500, 100)
	withRefund.CoreQuote = 80
	withRefund.RefundRequestedUSD = 20
	core := quoteRequest("B", 500, 20)
	plan := (ProducerResourceCoordinator{}).Allocate(s, []ProducerResourceRequest{withRefund, core}, true)
	if len(plan.Allocations) != 2 || !almostEqual(plan.Allocations[0].AllocatedQuote, 80) || !almostEqual(plan.Allocations[1].AllocatedQuote, 20) {
		t.Fatalf("refund displaced core: %+v", plan.Allocations)
	}
}

func recoverySnapshot(side OrderSide) Case3AObligationSnapshot {
	return Case3AObligationSnapshot{ObligationID: "r", OriginDecisionID: "d", SourceEntryOrderID: "e", SourceExitOrderID: "x", Side: side, RecoveryMethod: RecoveryByProfitTarget, TargetPrice: 100, RemainingBase: 1, RecoveryRemainingUSD: 2, ProfitGateUSD: 1, Status: Case3AObligationWaitingForTarget}
}

func TestVersion201RecoveryFeeSlippageTargetGolden(t *testing.T) {
	g := loadVersion201Golden(t)
	if got := evaluateCase3AObligationResurrections(g.Recovery.SellBelowTrigger, g.Recovery.TakerFeeRate, []Case3AObligationSnapshot{recoverySnapshot(SideSell)}); len(got) != 0 {
		t.Fatalf("SELL resurrected below target: %+v", got)
	}
	if got := evaluateCase3AObligationResurrections(g.Recovery.SellAtTrigger, g.Recovery.TakerFeeRate, []Case3AObligationSnapshot{recoverySnapshot(SideSell)}); len(got) != 1 || got[0].Signal != Sell {
		t.Fatalf("SELL did not resurrect: %+v", got)
	}
	if got := evaluateCase3AObligationResurrections(g.Recovery.BuyAboveTrigger, g.Recovery.TakerFeeRate, []Case3AObligationSnapshot{recoverySnapshot(SideBuy)}); len(got) != 0 {
		t.Fatalf("BUY resurrected above target: %+v", got)
	}
	if got := evaluateCase3AObligationResurrections(g.Recovery.BuyAtTrigger, g.Recovery.TakerFeeRate, []Case3AObligationSnapshot{recoverySnapshot(SideBuy)}); len(got) != 1 || got[0].Signal != Buy {
		t.Fatalf("BUY did not resurrect: %+v", got)
	}
}

func TestVersion201AdmissionGolden(t *testing.T) {
	now := time.Date(2026, 9, 11, 0, 0, 0, 0, time.UTC)
	tr := &Trader{MarketRegime: RegimeDown, lastExits: []ExitRecord{{Time: now.Add(-time.Hour), Side: SideBuy, ClosePrice: 100, PNLUSD: -1, Reason: "threshold_stop_loss"}}}
	blocked := tr.evaluateProducerAdmissionLocked(EntryDecision{Signal: Buy, Producer: EntryProducerCase11BBottomReversal, ProducerTier: ProducerTierMid}, 101, now)
	if blocked.Allowed || blocked.ErrorCode != EntryProduceErrDecisionCase3BBlocked {
		t.Fatalf("Case3B BUY not blocked: %+v", blocked)
	}
	allowed := tr.evaluateProducerAdmissionLocked(EntryDecision{Signal: Buy, Producer: EntryProducerCase16BNormalBottomRolloverBuy, ProducerTier: ProducerTierLow}, 101, now)
	if !allowed.Allowed {
		t.Fatalf("LOW producer did not bypass Case3B: %+v", allowed)
	}
	tr.cfg.LongOnly = true
	longOnly := tr.evaluateProducerAdmissionLocked(EntryDecision{Signal: Sell, Producer: EntryProducerCase16ANormalPeakRolloverSell, ProducerTier: ProducerTierLow}, 101, now)
	if longOnly.Allowed || longOnly.ErrorCode != EntryProduceErrDecisionLongOnlyBlocked {
		t.Fatalf("LongOnly did not block SELL: %+v", longOnly)
	}
}

func TestVersion201EntryPoliciesGolden(t *testing.T) {
	legacy := entryPolicyForSource(EntryProducerNormalLegacy)
	if !legacy.ResetLastAdd || !legacy.ResetWinExtreme || !legacy.ResetLatchedGate || !legacy.ResetRegime {
		t.Fatalf("legacy policy changed: %+v", legacy)
	}
	equity := entryPolicyForSource(EntryProducerEquity)
	if equity.ResetLastAdd || equity.ResetWinExtreme || equity.ResetLatchedGate || equity.ResetRegime {
		t.Fatalf("equity policy changed: %+v", equity)
	}
}

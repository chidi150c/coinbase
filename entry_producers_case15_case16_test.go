package main

import "testing"

func TestCase15Case16ProducerPriorities(t *testing.T) {
	want := map[EntryProducer]ProducerPriority{
		EntryProducerCase15BDowntrendRecoveryBuy: 99,
		EntryProducerCase16ANormalPeakRolloverSell: 98,
		EntryProducerCase16BNormalBottomRolloverBuy: 97,
		EntryProducerCase15AUptrendRecoverySell: 96,
	}
	for producer, priority := range want {
		if got := producerPriorityFor(producer); got != priority {
			t.Fatalf("producer %s priority=%d, want %d", producer, got, priority)
		}
		tier, _ := producerTierFor(producer)
		if tier != ProducerTierLow {
			t.Fatalf("producer %s tier=%s, want LOW", producer, tier)
		}
	}
}

func TestCase16ASuppliedSnapshotPasses(t *testing.T) {
	d := EntryDecision{}
	passed := applyCase16ANormalPeakRolloverSellProducer(
		&d,
		AIResult{Raw: Sell, Confidence: 0.59},
		MACDResult{LinePrev6: 39.81532, Line: 30.50008, Hist: -4.42483},
		EMAPatternResult{Spread: 0.000124, EMA2050: 0.000711},
		PyramidResult{Sell: PyramidSideResult{SpacingPass: true}},
		78881.995,
		78978.39,
		RegimeNormal,
		PendingProducerCounts{ByProducer: map[EntryProducer]map[OrderSide]int{}},
		ProducerContinuationReferences{},
	)
	if !passed {
		t.Fatal("supplied Case16A snapshot did not pass")
	}
	if d.Signal != Sell || d.Producer != EntryProducerCase16ANormalPeakRolloverSell {
		t.Fatalf("unexpected Case16A decision: signal=%s producer=%s", d.Signal, d.Producer)
	}
}

func TestCase16BMirrorPasses(t *testing.T) {
	d := EntryDecision{}
	passed := applyCase16BNormalBottomRolloverBuyProducer(
		&d,
		AIResult{Raw: Buy, Confidence: 0.59},
		MACDResult{LinePrev6: -39.81532, Line: -30.50008, Hist: 4.42483},
		EMAPatternResult{Spread: -0.000124, EMA2050: -0.000711},
		PyramidResult{Buy: PyramidSideResult{SpacingPass: true}},
		78881.995,
		78785.60,
		RegimeNormal,
		PendingProducerCounts{ByProducer: map[EntryProducer]map[OrderSide]int{}},
		ProducerContinuationReferences{},
	)
	if !passed || d.Signal != Buy || d.Producer != EntryProducerCase16BNormalBottomRolloverBuy {
		t.Fatalf("unexpected Case16B decision: passed=%t signal=%s producer=%s", passed, d.Signal, d.Producer)
	}
}

func TestCase15AMirrorPasses(t *testing.T) {
	d := EntryDecision{}
	passed := applyCase15AUptrendRecoverySellProducer(
		&d,
		AIResult{Raw: Sell, Confidence: 0.59},
		MACDResult{LinePrev6: 39.81532, Line: 30.50008, Hist: -4.42483},
		EMAPatternResult{PatternSell: true, PriceUpDown: true},
		PyramidResult{Sell: PyramidSideResult{Latched: 79000, SpacingPass: true}},
		78881.995,
		RegimeUp,
		PendingProducerCounts{ByProducer: map[EntryProducer]map[OrderSide]int{}},
		ProducerContinuationReferences{},
	)
	if !passed || d.Signal != Sell || d.Producer != EntryProducerCase15AUptrendRecoverySell {
		t.Fatalf("unexpected Case15A decision: passed=%t signal=%s producer=%s", passed, d.Signal, d.Producer)
	}
}

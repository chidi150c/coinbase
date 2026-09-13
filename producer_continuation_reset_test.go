package main

import "testing"

func TestContinuationResetSignalContracts(t *testing.T) {
	tests := []struct {
		name       string
		producer   EntryProducer
		side       OrderSide
		reset      Signal
		preserving Signal
	}{
		{"normal buy", EntryProducerNormalLegacy, SideBuy, Sell, Buy},
		{"normal sell", EntryProducerNormalLegacy, SideSell, Buy, Sell},
		{"equity buy", EntryProducerEquity, SideBuy, Sell, Buy},
		{"equity sell", EntryProducerEquity, SideSell, Buy, Sell},
		{"case11a sell", EntryProducerCase11APeakReversal, SideSell, Buy, Sell},
		{"case11b buy", EntryProducerCase11BBottomReversal, SideBuy, Sell, Buy},
		{"case14b buy", EntryProducerCase14BUptrendBuy, SideBuy, Sell, Buy},
		{"case13a sell", EntryProducerCase13APeakSell, SideSell, Sell, Buy},
		{"case13b buy", EntryProducerCase13BBottomBuy, SideBuy, Buy, Sell},
		{"case15a sell", EntryProducerCase15AUptrendRecoverySell, SideSell, Sell, Buy},
		{"case15b buy", EntryProducerCase15BDowntrendRecoveryBuy, SideBuy, Buy, Sell},
		{"case16a sell", EntryProducerCase16ANormalPeakRolloverSell, SideSell, Sell, Buy},
		{"case16b buy", EntryProducerCase16BNormalBottomRolloverBuy, SideBuy, Buy, Sell},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			resetSignal, configured :=
				continuationResetSignalFor(tt.producer, tt.side)
			if !configured {
				t.Fatal("continuation reset policy is not configured")
			}
			if resetSignal != tt.reset {
				t.Fatalf("reset signal=%s want=%s", resetSignal, tt.reset)
			}

			trader := &Trader{
				producerContinuationReferences: ProducerContinuationReferences{
					tt.producer: map[OrderSide]float64{tt.side: 100},
				},
			}

			trader.resetProducerContinuationReferences(tt.preserving)
			if got := trader.producerContinuationReferences.Reference(tt.producer, tt.side); got != 100 {
				t.Fatalf("preserving signal removed reference: got=%v", got)
			}

			trader.resetProducerContinuationReferences(Flat)
			if got := trader.producerContinuationReferences.Reference(tt.producer, tt.side); got != 100 {
				t.Fatalf("FLAT removed reference: got=%v", got)
			}

			trader.resetProducerContinuationReferences(tt.reset)
			if got := trader.producerContinuationReferences.Reference(tt.producer, tt.side); got != 0 {
				t.Fatalf("reset signal retained reference: got=%v", got)
			}
		})
	}
}

func TestContinuationResetDoesNotClearUnrelatedSameSideProducer(t *testing.T) {
	trader := &Trader{
		producerContinuationReferences: ProducerContinuationReferences{
			EntryProducerCase11APeakReversal: map[OrderSide]float64{
				SideSell: 100,
			},
			EntryProducerCase13APeakSell: map[OrderSide]float64{
				SideSell: 200,
			},
		},
	}

	trader.resetProducerContinuationReferences(Buy)

	if got := trader.producerContinuationReferences.Reference(
		EntryProducerCase11APeakReversal,
		SideSell,
	); got != 0 {
		t.Fatalf("Case11A SELL reference survived AI BUY: got=%v", got)
	}

	if got := trader.producerContinuationReferences.Reference(
		EntryProducerCase13APeakSell,
		SideSell,
	); got != 200 {
		t.Fatalf("Case13A SELL reference was cleared by its supporting AI BUY: got=%v", got)
	}
}

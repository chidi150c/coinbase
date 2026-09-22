package main

import (
	"context"
	"fmt"
	"math"
	"sync"
	"testing"
	"time"
)

type resetTestBroker struct {
	mu        sync.Mutex
	price     float64
	quote     float64
	base      float64
	open      map[string]bool
	lastSide  OrderSide
	lastQuote float64
}

func (b *resetTestBroker) Name() string { return "reset-test" }
func (b *resetTestBroker) GetNowPrice(context.Context, string) (float64, error) {
	return b.price, nil
}
func (b *resetTestBroker) GetRecentCandles(context.Context, string, string, int) ([]Candle, error) {
	return nil, nil
}
func (b *resetTestBroker) GetAvailableBase(context.Context, string) (string, float64, float64, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return "BTC", b.base, 0.00000001, nil
}
func (b *resetTestBroker) GetAvailableQuote(context.Context, string) (string, float64, float64, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return "USDT", b.quote, 0.01, nil
}
func (b *resetTestBroker) GetExchangeFilters(context.Context, string) (ExFilters, error) {
	return ExFilters{BaseStep: 0.00000001, QuoteStep: 0.01, MinNotional: 10}, nil
}
func (b *resetTestBroker) GetBBO(context.Context, string) (float64, float64, error) {
	return b.price, b.price, nil
}
func (b *resetTestBroker) PlaceMarketQuote(ctx context.Context, product string, side OrderSide, quote float64) (*PlacedOrder, error) {
	return b.PlaceMarketQuoteWithClientID(ctx, product, side, quote, "")
}
func (b *resetTestBroker) PlaceMarketQuoteWithClientID(_ context.Context, _ string, side OrderSide, quote float64, _ string) (*PlacedOrder, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.lastSide, b.lastQuote = side, quote
	base := quote / b.price
	if side == SideBuy {
		b.quote -= quote
		b.base += base
	} else {
		b.quote += quote
		b.base -= base
	}
	return &PlacedOrder{ID: "reset-order", Status: "done", BaseSize: base, QuoteSpent: quote, Price: b.price}, nil
}
func (b *resetTestBroker) PlaceLimitPostOnly(context.Context, string, OrderSide, float64, float64) (string, error) {
	return "", fmt.Errorf("unexpected limit order")
}
func (b *resetTestBroker) PlaceLimitPostOnlyWithClientID(context.Context, string, OrderSide, float64, float64, string) (string, error) {
	return "", fmt.Errorf("unexpected limit order")
}
func (b *resetTestBroker) GetOrder(_ context.Context, _ string, id string) (*PlacedOrder, error) {
	return &PlacedOrder{ID: id, Status: "done"}, nil
}
func (b *resetTestBroker) CancelOrder(_ context.Context, _ string, id string) error {
	b.mu.Lock()
	defer b.mu.Unlock()
	delete(b.open, id)
	return nil
}
func (b *resetTestBroker) ListOpenOrderIDs(context.Context, string) ([]string, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	ids := make([]string, 0, len(b.open))
	for id := range b.open {
		ids = append(ids, id)
	}
	return ids, nil
}

func newResetTestTrader(b *resetTestBroker) *Trader {
	return NewTrader(Config{
		ProductID:       "BTC-USDT",
		OrderMinUSD:     10,
		PersistState:    false,
		AIFeatureDim:    8,
		ProducerHistoryFile: "",
	}, b)
}

func TestRebalanceBalancesFiftyFiftyBuysWithExistingMarketPath(t *testing.T) {
	b := &resetTestBroker{price: 100, quote: 800, base: 2, open: map[string]bool{}}
	trader := newResetTestTrader(b)
	after, err := trader.rebalanceBalancesFiftyFifty(context.Background(), 100, "buy-test")
	if err != nil {
		t.Fatal(err)
	}
	if b.lastSide != SideBuy || math.Abs(b.lastQuote-300) > 1e-9 {
		t.Fatalf("order side=%s quote=%.8f, want BUY 300", b.lastSide, b.lastQuote)
	}
	if math.Abs(after.AvailQuote-after.AvailBase*100) > 0.01 {
		t.Fatalf("not balanced: quote=%.8f base_value=%.8f", after.AvailQuote, after.AvailBase*100)
	}
}

func TestRebalanceBalancesFiftyFiftySellsWithExistingMarketPath(t *testing.T) {
	b := &resetTestBroker{price: 100, quote: 200, base: 8, open: map[string]bool{}}
	trader := newResetTestTrader(b)
	after, err := trader.rebalanceBalancesFiftyFifty(context.Background(), 100, "sell-test")
	if err != nil {
		t.Fatal(err)
	}
	if b.lastSide != SideSell || math.Abs(b.lastQuote-300) > 1e-9 {
		t.Fatalf("order side=%s quote=%.8f, want SELL 300", b.lastSide, b.lastQuote)
	}
	if math.Abs(after.AvailQuote-after.AvailBase*100) > 0.01 {
		t.Fatalf("not balanced: quote=%.8f base_value=%.8f", after.AvailQuote, after.AvailBase*100)
	}
}

func TestCancelAndReconcileAllOrdersUsesPerOrderBrokerPath(t *testing.T) {
	b := &resetTestBroker{price: 100, quote: 500, base: 5, open: map[string]bool{"exchange-only": true}}
	trader := newResetTestTrader(b)
	trader.pendingExits["known"] = &PendingExit{OrderID: "known", ProductID: "BTC-USDT"}
	b.open["known"] = true
	if err := trader.cancelAndReconcileAllOrders(context.Background()); err != nil {
		t.Fatal(err)
	}
	if len(b.open) != 0 {
		t.Fatalf("open orders remain: %v", b.open)
	}
}

func TestInstallCleanResetStatePreservesModelWeightsAndLastFit(t *testing.T) {
	b := &resetTestBroker{price: 100, quote: 500, base: 5, open: map[string]bool{}}
	trader := newResetTestTrader(b)
	trader.model = NewLogisticModel(3)
	trader.model.W = []float64{0.25, -0.5, 0.75}
	trader.model.B = 0.125
	lastFit := time.Date(2026, 9, 22, 12, 0, 0, 0, time.UTC)
	trader.lastFit = lastFit

	err := trader.installCleanResetState(balanceSnapshot{
		SymQuote: "USDT", AvailQuote: 500, QuoteStep: 0.01,
		SymBase: "BTC", AvailBase: 5, BaseStep: 0.00000001,
		UpdatedAt: time.Now(),
	}, 100)
	if err != nil {
		t.Fatal(err)
	}
	if trader.lastFit != lastFit {
		t.Fatalf("lastFit changed during reset: got %s want %s", trader.lastFit, lastFit)
	}
	want := []float64{0.25, -0.5, 0.75}
	if len(trader.model.W) != len(want) {
		t.Fatalf("weight count=%d want=%d", len(trader.model.W), len(want))
	}
	for i := range want {
		if trader.model.W[i] != want[i] {
			t.Fatalf("weight[%d]=%.8f want %.8f", i, trader.model.W[i], want[i])
		}
	}
	if trader.model.B != 0.125 {
		t.Fatalf("bias=%.8f want 0.125", trader.model.B)
	}
}
